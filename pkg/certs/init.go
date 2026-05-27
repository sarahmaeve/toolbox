package certs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrMkcertNotFound is returned by Init when `mkcert` is not on PATH.
// Callers wrap this with their own install hint.
var ErrMkcertNotFound = errors.New("mkcert not found on PATH")

// InitOptions drives Init. Stderr receives progress lines; pass
// io.Discard in tests or quiet callers.
type InitOptions struct {
	Stderr io.Writer
}

// InitResult reports what Init did. Actions is a human-readable audit
// trail suitable for printing verbatim.
type InitResult struct {
	CertDir string
	CAPath  string
	Actions []string
}

// Init copies mkcert's root CA into a stable, project-owned location
// so the env var can point at a path that doesn't move when mkcert
// updates or relocates its CAROOT.
//
// Idempotent: safe to re-run. If mkcert has rotated its CA, the
// managed copy is refreshed; if not, Init is effectively a no-op.
//
// Init does NOT generate the localhost server cert — that's
// `mkcert 127.0.0.1 localhost` invoked by a separate setup step. This
// function's sole responsibility is making the CA trust file reachable
// at a stable path.
func (m *Manager) Init(opts InitOptions) (*InitResult, error) {
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	certDirResolved, err := expandHome(m.cfg.CertDir)
	if err != nil {
		return nil, fmt.Errorf("resolve cert dir %q: %w", m.cfg.CertDir, err)
	}

	result := &InitResult{
		CertDir: certDirResolved,
		CAPath:  filepath.Join(certDirResolved, m.cfg.CAFileName),
	}

	sourceCA, err := mkcertCARoot()
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(opts.Stderr, "mkcert CA: %s\n", sourceCA)

	if err := os.MkdirAll(certDirResolved, 0o750); err != nil {
		return nil, fmt.Errorf("create cert dir %q: %w", certDirResolved, err)
	}
	result.Actions = append(result.Actions,
		fmt.Sprintf("ensured cert dir %s", certDirResolved))

	// Copy unconditionally on every run so mkcert reinstalls propagate
	// to the managed copy without the user having to know that
	// divergence is what's breaking TLS.
	if err := copyFile(sourceCA, result.CAPath); err != nil {
		return nil, fmt.Errorf("copy CA %s → %s: %w", sourceCA, result.CAPath, err)
	}
	result.Actions = append(result.Actions,
		fmt.Sprintf("copied CA %s → %s", sourceCA, result.CAPath))
	_, _ = fmt.Fprintf(opts.Stderr, "wrote %s\n", result.CAPath)

	return result, nil
}

// copyFile writes src's contents to dst with 0o600 mode via temp file
// + rename for atomicity — a crash mid-copy leaves the previous valid
// CA in place rather than a truncated file that would silently break
// TLS on next handshake.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // G304: path from mkcert discovery, trusted
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil { //nolint:gosec // G703: dst flows from operator config
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp to dst: %w", err)
	}
	return nil
}

// mkcertCARoot discovers the mkcert-managed rootCA.pem path on the
// current machine. Overridden by setMkcertCAForTest in tests.
//
// Package-level var so tests can swap the implementation without
// mutating real OS state.
var mkcertCARoot = defaultMkcertCARoot

func defaultMkcertCARoot() (string, error) {
	bin, err := exec.LookPath("mkcert")
	if err != nil {
		return "", ErrMkcertNotFound
	}
	out, err := exec.Command(bin, "-CAROOT").Output() //nolint:gosec // G204: bin resolved from LookPath, no user input
	if err != nil {
		return "", fmt.Errorf("run `mkcert -CAROOT`: %w", err)
	}
	caDir := strings.TrimSpace(string(out))
	if caDir == "" {
		return "", errors.New("mkcert -CAROOT returned empty output")
	}
	caPath := filepath.Join(caDir, "rootCA.pem")
	if _, err := os.Stat(caPath); err != nil {
		return "", fmt.Errorf("mkcert CA not found at %s (run `mkcert -install` first): %w", caPath, err)
	}
	return caPath, nil
}

// setMkcertCAForTest installs fn as the mkcert discovery hook and
// returns a restore function. Tests should always schedule the restore
// via t.Cleanup to prevent cross-test pollution.
func setMkcertCAForTest(fn func() (string, error)) func() {
	orig := mkcertCARoot
	mkcertCARoot = fn
	return func() { mkcertCARoot = orig }
}
