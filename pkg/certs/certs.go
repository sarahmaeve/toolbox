// Package certs manages a local TLS trust setup for projects that need
// a locally-trusted CA wired into agent-facing HTTPS clients. The
// motivating use case: Claude Code's WebFetch (and similar Node-backed
// HTTP clients) forces HTTPS on every URL, so a local-only service
// must present a cert that Node trusts. mkcert creates a locally-
// trusted CA and issues a localhost cert against it; Node's TLS stack
// consults NODE_EXTRA_CA_CERTS at every handshake to load additional
// trusted CAs.
//
// The failure mode that drove this package into existence: an ambient
// env var like NODE_EXTRA_CA_CERTS vanishes across terminal restarts,
// GUI launches, and any subagent that inherits a clean env. The
// dispatch would succeed once and fail the next with "unable to
// verify the first certificate."
//
// Three operations:
//
//   - Check: non-interactive preflight. Reports OK or a typed failure
//     code with a remediation hint.
//   - Init: idempotent setup. Copies mkcert's root CA to a stable,
//     project-owned path so the env var target stays put across mkcert
//     reinstalls.
//   - WriteProfile: opt-in shell profile patching. Appends a bracketed
//     managed block to the user's shell profile so the env var
//     persists across sessions.
//
// This package was extracted from signatory's internal/certs; the
// signatory-specific env var name and directory are now Config fields
// so any project can plug in its own values.
package certs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config configures a Manager. Pass a Config to New; defaults are
// applied to empty fields.
type Config struct {
	// EnvVar is the environment variable Node's TLS stack (or any
	// equivalent client) reads at every HTTPS handshake to locate
	// additional trusted CA bundles. Required.
	//
	// Common values: "NODE_EXTRA_CA_CERTS" for Node-backed clients
	// (Claude Code's WebFetch), "SSL_CERT_FILE" for OpenSSL-based
	// clients, "CURL_CA_BUNDLE" for curl.
	EnvVar string

	// CertDir is the directory where the managed CA copy lives.
	// Tilde-prefixed paths ("~/.foo") are expanded against
	// os.UserHomeDir(). Defaults to "~/.toolbox/certs" if empty.
	CertDir string

	// CAFileName is the filename under CertDir that EnvVar points at.
	// Defaults to "rootCA.pem" if empty.
	CAFileName string

	// ProfileMarkerBegin / ProfileMarkerEnd bracket the managed block
	// inside the shell profile. Re-running WriteProfile replaces
	// whatever lives between them, so changing these markers across
	// versions will orphan previously-written blocks. Defaults render
	// a self-documenting comment naming EnvVar.
	ProfileMarkerBegin string
	ProfileMarkerEnd   string

	// ShellProfile is the path to patch. Defaults to "~/.zshrc".
	// zshrc is the interactive-shell entry point on macOS and matches
	// how Claude Code is typically launched (via an interactive
	// terminal). zprofile and zshenv are deliberately not the default:
	// zprofile may be skipped by GUI-launched apps; zshenv runs for
	// every shell including non-interactive ones.
	ShellProfile string
}

// Manager owns the operations against a configured cert setup.
// Construct via New.
type Manager struct {
	cfg Config
}

// New returns a Manager with the given config. EnvVar is required;
// empty CertDir / CAFileName / ProfileMarker* / ShellProfile take
// sensible defaults derived from EnvVar.
//
// Panics if EnvVar is empty — this is a programmer error caught at
// construction, not a runtime failure.
func New(cfg Config) *Manager {
	if cfg.EnvVar == "" {
		panic("certs: Config.EnvVar is required")
	}
	if cfg.CertDir == "" {
		cfg.CertDir = "~/.toolbox/certs"
	}
	if cfg.CAFileName == "" {
		cfg.CAFileName = "rootCA.pem"
	}
	if cfg.ProfileMarkerBegin == "" {
		cfg.ProfileMarkerBegin = fmt.Sprintf(
			"# toolbox-managed: %s — BEGIN (regenerate via your project's certs init; remove this whole block to detach)",
			cfg.EnvVar)
	}
	if cfg.ProfileMarkerEnd == "" {
		cfg.ProfileMarkerEnd = fmt.Sprintf("# toolbox-managed: %s — END", cfg.EnvVar)
	}
	if cfg.ShellProfile == "" {
		cfg.ShellProfile = "~/.zshrc"
	}
	return &Manager{cfg: cfg}
}

// Config returns a copy of the Manager's configuration.
func (m *Manager) Config() Config { return m.cfg }

// CAPath returns the absolute path of the canonical CA anchor the
// Manager manages: CertDir + CAFileName with `~/` expanded.
//
// Returns an error only if the user has no HOME — an exceptional
// environment in which almost nothing else would work either. Callers
// that can proceed without the anchor (e.g., HTTPS clients that fall
// back to the system root pool) should tolerate the error by skipping
// the load rather than aborting.
func (m *Manager) CAPath() (string, error) {
	return expandHome(m.cfg.CertDir + "/" + m.cfg.CAFileName)
}

// FailCode classifies why Check reported NotOK. Callers use it to pick
// an exit code and remediation hint; humans use the Message + Fix
// fields of CheckResult for the actual message text.
type FailCode int

const (
	// StatusOK means the preflight passed — env var is set, points to
	// a readable file that looks like a PEM cert bundle.
	StatusOK FailCode = iota

	// FailEnvUnset means the env var is empty or unset in the current
	// process env. Most common failure mode — the ambient-env problem
	// this package was built to close.
	FailEnvUnset

	// FailPathMissing means the env var points at a filesystem path
	// that doesn't exist. Usually happens after mkcert is reinstalled
	// into a different CAROOT without rerunning Init.
	FailPathMissing

	// FailPathInvalid means the path exists but its contents don't
	// parse as PEM — truncation, wrong file, or a different format
	// entirely.
	FailPathInvalid
)

// ProfileAction names the mutation WriteProfile applied.
type ProfileAction string

const (
	ProfileUnchanged ProfileAction = "unchanged"
	ProfileAppended  ProfileAction = "appended"
	ProfileReplaced  ProfileAction = "replaced"
	ProfileCreated   ProfileAction = "created"
)

// expandHome resolves a leading `~/` or bare `~` to the user's home
// directory. Returns the input unchanged if it doesn't start with `~`.
// Absolute paths round-trip cleanly.
func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:]), nil
	}
	// `~otheruser/...` is not supported.
	return p, nil
}
