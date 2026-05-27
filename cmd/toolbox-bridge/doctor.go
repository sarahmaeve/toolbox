package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sarahmaeve/toolbox/pkg/certs"
)

// probeResult is one entry in the doctor's report.
type probeResult struct {
	Name    string
	Status  probeStatus
	Message string
	Fix     string
}

type probeStatus int

const (
	probePass probeStatus = iota
	probeFail
	probeInfo
)

func (s probeStatus) label() string {
	switch s {
	case probePass:
		return "PASS"
	case probeFail:
		return "FAIL"
	default:
		return "INFO"
	}
}

// runDoctor runs the breadth-first diagnostic and prints a report.
// Returns a non-zero error only when --strict is set and any probe
// failed; without --strict, doctor is informational and exits 0 so
// it can be used in shell pipelines.
func runDoctor(args []string) error {
	fset := flag.NewFlagSet("doctor", flag.ContinueOnError)
	strict := fset.Bool("strict", false, "exit non-zero if any probe failed")
	pidPath := fset.String("pid-file", defaultPIDPath, "PID file location for the daemon-status probe")
	if err := fset.Parse(args); err != nil {
		return err
	}

	results := []probeResult{
		probeMkcertOnPath(),
		probeMkcertCARoot(),
		probeManagedCA(),
		probeServerCert(),
		probeEnvVar(),
		probeDatabase(),
		probeSchemasDir(),
		probeDaemonStatus(*pidPath),
	}

	maxName := 0
	for _, r := range results {
		if n := len(r.Name); n > maxName {
			maxName = n
		}
	}

	fails := 0
	for _, r := range results {
		fmt.Printf("%-4s  %-*s  %s\n", r.Status.label(), maxName, r.Name, r.Message)
		if r.Status == probeFail {
			fails++
			if r.Fix != "" {
				fmt.Printf("        %s  fix: %s\n", strings.Repeat(" ", maxName), r.Fix)
			}
		}
	}

	if fails > 0 && *strict {
		return fmt.Errorf("%d probe(s) failed", fails)
	}
	return nil
}

// --- probes ----------------------------------------------------------------

func probeMkcertOnPath() probeResult {
	if path, err := exec.LookPath("mkcert"); err == nil {
		return probeResult{Name: "mkcert-on-path", Status: probePass, Message: path}
	}
	return probeResult{
		Name:    "mkcert-on-path",
		Status:  probeFail,
		Message: "mkcert not found on PATH",
		Fix:     "install mkcert: `brew install mkcert` (macOS) / `sudo apt install mkcert` (Debian) / `sudo pacman -S mkcert` (Arch)",
	}
}

func probeMkcertCARoot() probeResult {
	mkcertBin, err := exec.LookPath("mkcert")
	if err != nil {
		return probeResult{Name: "mkcert-ca-installed", Status: probeInfo, Message: "skipped (mkcert not on PATH)"}
	}
	out, err := exec.Command(mkcertBin, "-CAROOT").Output() //nolint:gosec // G204: mkcertBin from exec.LookPath
	if err != nil {
		return probeResult{
			Name: "mkcert-ca-installed", Status: probeFail,
			Message: fmt.Sprintf("mkcert -CAROOT failed: %v", err),
			Fix:     "run `mkcert -install` once to wire your system trust store",
		}
	}
	caDir := strings.TrimSpace(string(out))
	caPath := filepath.Join(caDir, "rootCA.pem")
	if _, err := os.Stat(caPath); err != nil {
		return probeResult{
			Name: "mkcert-ca-installed", Status: probeFail,
			Message: fmt.Sprintf("rootCA.pem not found at %s", caPath),
			Fix:     "run `mkcert -install` once to wire your system trust store",
		}
	}
	return probeResult{Name: "mkcert-ca-installed", Status: probePass, Message: caPath}
}

func probeManagedCA() probeResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return probeResult{Name: "managed-ca", Status: probeFail, Message: "$HOME not set"}
	}
	p := filepath.Join(home, ".toolbox", "certs", "rootCA.pem")
	if _, err := os.Stat(p); err != nil {
		return probeResult{
			Name:    "managed-ca",
			Status:  probeFail,
			Message: fmt.Sprintf("missing: %s", p),
			Fix:     "run `toolbox-bridge init` to copy the mkcert CA into the managed path",
		}
	}
	return probeResult{Name: "managed-ca", Status: probePass, Message: p}
}

func probeServerCert() probeResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return probeResult{Name: "server-cert", Status: probeFail, Message: "$HOME not set"}
	}
	cert := filepath.Join(home, ".toolbox", "certs", "127.0.0.1+1.pem")
	key := filepath.Join(home, ".toolbox", "certs", "127.0.0.1+1-key.pem")
	if !statExists(cert) || !statExists(key) {
		return probeResult{
			Name:    "server-cert",
			Status:  probeFail,
			Message: fmt.Sprintf("missing cert/key at %s (+ -key)", cert),
			Fix:     "run `toolbox-bridge init` (or `--skip-server-cert` then generate by hand)",
		}
	}
	return probeResult{Name: "server-cert", Status: probePass, Message: cert}
}

func probeEnvVar() probeResult {
	m := certs.New(certs.Config{EnvVar: EnvVar, CertDir: CertDir})
	r := m.Check()
	if r.OK {
		return probeResult{Name: EnvVar, Status: probePass, Message: r.Message}
	}
	return probeResult{
		Name:    EnvVar,
		Status:  probeFail,
		Message: r.Message,
		Fix:     r.Fix,
	}
}

func probeDatabase() probeResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return probeResult{Name: "database", Status: probeFail, Message: "$HOME not set"}
	}
	p := filepath.Join(home, ".toolbox", "messages.db")
	info, err := os.Stat(p)
	if err != nil {
		return probeResult{
			Name:    "database",
			Status:  probeInfo,
			Message: fmt.Sprintf("not yet created (%s)", p),
			Fix:     "`toolbox-bridge init` or first `serve run` will create it",
		}
	}
	return probeResult{
		Name:    "database",
		Status:  probePass,
		Message: fmt.Sprintf("%s (%d bytes)", p, info.Size()),
	}
}

func probeSchemasDir() probeResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return probeResult{Name: "schemas-dir", Status: probeFail, Message: "$HOME not set"}
	}
	p := filepath.Join(home, ".toolbox", "schemas")
	entries, err := os.ReadDir(p)
	if err != nil {
		return probeResult{
			Name:    "schemas-dir",
			Status:  probeInfo,
			Message: fmt.Sprintf("not present (%s)", p),
			Fix:     "run `toolbox-bridge init --seed-schemas` to seed the example schemas",
		}
	}
	jsonCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount == 0 {
		return probeResult{
			Name:    "schemas-dir",
			Status:  probeInfo,
			Message: fmt.Sprintf("%s exists but contains no *.json schemas", p),
			Fix:     "drop your MessageType schemas in, or rerun `toolbox-bridge init --seed-schemas`",
		}
	}
	return probeResult{
		Name:    "schemas-dir",
		Status:  probePass,
		Message: fmt.Sprintf("%s (%d schemas)", p, jsonCount),
	}
}

func probeDaemonStatus(pidPathFlag string) probeResult {
	resolved, err := expandHome(pidPathFlag)
	if err != nil {
		return probeResult{Name: "daemon", Status: probeInfo, Message: "could not resolve pid path"}
	}
	pid, alive := readLivePID(resolved)
	if !alive {
		if pid > 0 {
			return probeResult{
				Name:    "daemon",
				Status:  probeInfo,
				Message: fmt.Sprintf("stale pidfile (%s names dead pid %d)", resolved, pid),
				Fix:     "`toolbox-bridge serve stop --pid-file " + resolved + "` to clean up, then `serve start`",
			}
		}
		return probeResult{Name: "daemon", Status: probeInfo, Message: "not running"}
	}
	return probeResult{
		Name:    "daemon",
		Status:  probePass,
		Message: fmt.Sprintf("running (pid %d, %s)", pid, resolved),
	}
}
