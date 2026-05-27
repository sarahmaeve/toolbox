package certs

import (
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CheckResult is the outcome of a preflight check. Callers map OK=true
// to exit 0 and OK=false to a non-zero exit plus the printed Message
// and Fix. Fix is always populated on failure — a preflight that
// reports a problem without a remediation would leave the user worse
// off than no preflight at all.
type CheckResult struct {
	OK      bool
	Env     string // raw env-var value as seen by this process
	CAPath  string // expanded/absolute resolution of Env (empty if Env was empty)
	Code    FailCode
	Message string // one-line status (OK or concrete failure)
	Fix     string // remediation hint; empty when OK
}

// Check validates the current process env. Non-interactive and
// side-effect-free — safe to call from a server startup hook, a CI
// job, or an interactive command.
func (m *Manager) Check() CheckResult { return m.CheckWithEnv(os.Getenv) }

// CheckWithEnv is the seam tests drive. Production callers use Check;
// tests pass a synthesized env lookup so they don't depend on whatever
// is set in the runner's environment.
func (m *Manager) CheckWithEnv(getenv func(string) string) CheckResult {
	envVar := m.cfg.EnvVar
	raw := strings.TrimSpace(getenv(envVar))
	if raw == "" {
		return CheckResult{
			Code:    FailEnvUnset,
			Message: fmt.Sprintf("%s is not set — HTTPS clients cannot verify the local service's TLS cert", envVar),
			Fix:     fmt.Sprintf("run your project's `certs init --write-profile` to install the CA and persist %s, then restart your terminal", envVar),
		}
	}

	path, err := expandHome(raw)
	if err != nil {
		return CheckResult{
			Env:     raw,
			CAPath:  raw,
			Code:    FailPathMissing,
			Message: fmt.Sprintf("cannot resolve %s=%q: %v", envVar, raw, err),
			Fix:     fmt.Sprintf("set %s to an absolute path (no `~/`) or ensure $HOME is set", envVar),
		}
	}

	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return CheckResult{
			Env:     raw,
			CAPath:  path,
			Code:    FailPathMissing,
			Message: fmt.Sprintf("%s=%s but that file does not exist", envVar, path),
			Fix:     fmt.Sprintf("run your project's `certs init` to regenerate the managed CA at this path, or point %s at a real mkcert rootCA.pem", envVar),
		}
	case err != nil:
		return CheckResult{
			Env:     raw,
			CAPath:  path,
			Code:    FailPathInvalid,
			Message: fmt.Sprintf("%s=%s: stat failed: %v", envVar, path, err),
			Fix:     "check file permissions on the CA path and the directories leading to it",
		}
	case info.IsDir():
		return CheckResult{
			Env:     raw,
			CAPath:  path,
			Code:    FailPathInvalid,
			Message: fmt.Sprintf("%s=%s is a directory — the env var must point at the CA file, not its parent", envVar, path),
			Fix:     fmt.Sprintf("set %s to %s/%s (or run `certs init`)", envVar, path, m.cfg.CAFileName),
		}
	}

	// File exists; verify it at least superficially looks like a PEM
	// certificate. We don't validate the signature chain or expiry —
	// that's the TLS stack's job at handshake time.
	data, err := os.ReadFile(path) //nolint:gosec // G304: path resolved from user-controlled env, already stat'd
	if err != nil {
		return CheckResult{
			Env:     raw,
			CAPath:  path,
			Code:    FailPathInvalid,
			Message: fmt.Sprintf("%s=%s: read failed: %v", envVar, path, err),
			Fix:     "ensure the CA file is readable by your user (`chmod 0644 " + path + "`)",
		}
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return CheckResult{
			Env:     raw,
			CAPath:  path,
			Code:    FailPathInvalid,
			Message: fmt.Sprintf("%s=%s is not a PEM-encoded CERTIFICATE block", envVar, path),
			Fix:     "regenerate the managed copy with `certs init` — your CA file is truncated or the wrong format",
		}
	}

	return CheckResult{
		OK:      true,
		Env:     raw,
		CAPath:  path,
		Code:    StatusOK,
		Message: fmt.Sprintf("%s=%s (valid CERTIFICATE block)", envVar, path),
	}
}
