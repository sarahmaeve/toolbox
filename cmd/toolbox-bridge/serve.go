package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sarahmaeve/toolbox/pkg/bridge"
	"github.com/sarahmaeve/toolbox/pkg/messagestore"
	"github.com/sarahmaeve/toolbox/pkg/messagetypes"
	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// defaultPIDPath / defaultLogPath are the canonical locations for the
// daemon's lifecycle files. Configurable via flags but most operators
// can rely on the defaults.
const (
	defaultPIDPath = "~/.toolbox/run/bridge.pid"
	defaultLogPath = "~/.toolbox/log/bridge.log"
)

// serveConfig holds everything `serve run` and `serve start` need.
// Shared between foreground and background paths so flag handling
// stays in one place.
type serveConfig struct {
	dbPath       string
	port         int
	schemasDir   string
	allowedRoles string
	maxActive    int
	noTLS        bool
	certFile     string
	keyFile      string
	pidPath      string
	logPath      string
}

// runServe dispatches the `serve` subcommand to its sub-subcommand.
// If no sub-subcommand is given (or the first arg is a flag), this
// is a usage error — `serve run` is the explicit foreground command.
func runServe(args []string) error {
	if len(args) == 0 {
		return errors.New("serve requires a sub-subcommand: run | start | stop | restart | status")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "run":
		return runServeRun(rest)
	case "start":
		return runServeStart(rest)
	case "stop":
		return runServeStop(rest)
	case "restart":
		return runServeRestart(rest)
	case "status":
		return runServeStatus(rest)
	case "-h", "--help", "help":
		fmt.Println("serve: run | start | stop | restart | status")
		return nil
	default:
		return fmt.Errorf("unknown serve sub-subcommand: %s", sub)
	}
}

// parseServeFlags parses the shared flag set for run/start. stop /
// restart / status take a narrower set (parsed inline at their call
// sites) since they don't need to know about port/db/schemas.
func parseServeFlags(name string, args []string) (*serveConfig, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfg := &serveConfig{}
	fs.StringVar(&cfg.dbPath, "db", "~/.toolbox/messages.db", "path to SQLite database")
	fs.IntVar(&cfg.port, "port", 21518, "TCP port to bind (127.0.0.1)")
	fs.StringVar(&cfg.schemasDir, "schemas-dir", "", "directory of JSON Schema files; each <name>.json registers a MessageType named <name>")
	fs.StringVar(&cfg.allowedRoles, "allowed-roles", "", "comma-separated allowed Role values; empty means accept any role")
	fs.IntVar(&cfg.maxActive, "max-active-sessions", 100, "cap on concurrent active sessions (0 = unlimited)")
	fs.BoolVar(&cfg.noTLS, "no-tls", false, "serve plain HTTP — tests/smoke only")
	fs.StringVar(&cfg.certFile, "cert-file", "", "TLS cert path (defaults to ~/.toolbox/certs/127.0.0.1+1.pem)")
	fs.StringVar(&cfg.keyFile, "key-file", "", "TLS key path (defaults to ~/.toolbox/certs/127.0.0.1+1-key.pem)")
	fs.StringVar(&cfg.pidPath, "pid-file", defaultPIDPath, "PID file location (start/stop coordination)")
	fs.StringVar(&cfg.logPath, "log-file", defaultLogPath, "log file when running as daemon")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return cfg, nil
}

// runServeRun is the foreground path. Blocks until ctx is cancelled
// (SIGINT/SIGTERM) or the listener fails. Used directly via
// `serve run` and indirectly as the daemon child process.
func runServeRun(args []string) error {
	cfg, err := parseServeFlags("serve run", args)
	if err != nil {
		return err
	}

	resolvedDB, err := expandHome(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := messagestore.Open(ctx, messagestore.Config{
		DBPath:            resolvedDB,
		AllowedRoles:      splitCSV(cfg.allowedRoles),
		MaxActiveSessions: cfg.maxActive,
	})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close() //nolint:errcheck

	// Register canonical built-in types BEFORE applying --schemas-dir
	// so a user file cannot redefine (or shadow) them. RegisterType
	// returns ErrTypeAlreadyRegistered on a collision; we surface
	// any other failure as fatal since a built-in failing to register
	// indicates a programmer error in pkg/messagetypes.
	for _, mt := range messagetypes.Builtin() {
		if err := store.RegisterType(mt); err != nil {
			return fmt.Errorf("register built-in %q: %w", mt.Name, err)
		}
	}
	slog.Info("registered built-in message types", "count", len(messagetypes.Builtin()))

	if cfg.schemasDir != "" {
		n, err := loadSchemasDir(store, cfg.schemasDir)
		if err != nil {
			return fmt.Errorf("load schemas: %w", err)
		}
		slog.Info("registered message types", "count", n, "dir", cfg.schemasDir)
	} else {
		slog.Warn("no --schemas-dir set; no MessageTypes registered; deposits will fail with ErrUnknownType")
	}

	srv := bridge.NewServer(bridge.ServerConfig{Store: store})

	cert, key := cfg.certFile, cfg.keyFile
	if !cfg.noTLS {
		if cert == "" {
			cert, err = expandHome("~/.toolbox/certs/127.0.0.1+1.pem")
			if err != nil {
				return err
			}
		}
		if key == "" {
			key, err = expandHome("~/.toolbox/certs/127.0.0.1+1-key.pem")
			if err != nil {
				return err
			}
		}
		if _, err := os.Stat(cert); err != nil {
			return fmt.Errorf("cert file %s not found; run `mkcert 127.0.0.1 localhost` in ~/.toolbox/certs (or pass --no-tls)", cert)
		}
	} else {
		cert, key = "", ""
		slog.Warn("--no-tls: serving plain HTTP; agents that require HTTPS will not connect")
	}

	slog.Info("starting bridge",
		"port", cfg.port,
		"db", resolvedDB,
		"tls", !cfg.noTLS,
		"types", len(store.RegisteredTypes()))

	return srv.ListenAndServe(ctx, cfg.port, cert, key)
}

// runServeStart re-execs the binary as `serve run` with the supplied
// flags, detaches it from the controlling terminal, redirects its
// stdout/stderr to the log file, and writes the child PID to the
// PID file. Parent exits immediately on success.
//
// Refuses to start if the PID file already names a live process.
func runServeStart(args []string) error {
	cfg, err := parseServeFlags("serve start", args)
	if err != nil {
		return err
	}

	resolvedPID, err := expandHome(cfg.pidPath)
	if err != nil {
		return fmt.Errorf("resolve pid path: %w", err)
	}
	resolvedLog, err := expandHome(cfg.logPath)
	if err != nil {
		return fmt.Errorf("resolve log path: %w", err)
	}

	if pid, alive := readLivePID(resolvedPID); alive {
		return fmt.Errorf("bridge already running (pid %d at %s); use `serve stop` or `serve restart`",
			pid, resolvedPID)
	}

	for _, dir := range []string{filepath.Dir(resolvedPID), filepath.Dir(resolvedLog)} {
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: standard dir perms; dir houses operator-readable logs
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	logFile, err := os.OpenFile(resolvedLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: operator-supplied log path
	if err != nil {
		return fmt.Errorf("open log file %s: %w", resolvedLog, err)
	}
	// Note: logFile is intentionally NOT deferred-closed in the parent
	// — the child inherits the fd, and the parent's copy can close on
	// process exit without affecting the child.

	execArgs := buildRunArgs(cfg, args)
	cmd := exec.Command(os.Args[0], execArgs...) //nolint:gosec // G204: re-exec of our own binary with whitelisted flags
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close() //nolint:errcheck
		return fmt.Errorf("start daemon: %w", err)
	}

	// Capture PID before Release — recent Go versions invalidate
	// cmd.Process.Pid after Release.
	childPID := cmd.Process.Pid

	if err := os.WriteFile(resolvedPID, []byte(strconv.Itoa(childPID)+"\n"), 0o600); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck
		return fmt.Errorf("write pid file %s: %w", resolvedPID, err)
	}

	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release child: %w", err)
	}

	fmt.Printf("started bridge: pid %d, log %s, pidfile %s\n",
		childPID, resolvedLog, resolvedPID)
	return nil
}

// buildRunArgs returns the args slice to pass to `serve run` in the
// daemon child. It threads through every serve flag the parent saw,
// rebuilt from the parsed serveConfig so the child can't see flags it
// shouldn't (e.g., a stale --pid-file or --log-file).
func buildRunArgs(cfg *serveConfig, _ []string) []string {
	out := []string{"serve", "run",
		"--db", cfg.dbPath,
		"--port", strconv.Itoa(cfg.port),
		"--max-active-sessions", strconv.Itoa(cfg.maxActive),
	}
	if cfg.schemasDir != "" {
		out = append(out, "--schemas-dir", cfg.schemasDir)
	}
	if cfg.allowedRoles != "" {
		out = append(out, "--allowed-roles", cfg.allowedRoles)
	}
	if cfg.noTLS {
		out = append(out, "--no-tls")
	}
	if cfg.certFile != "" {
		out = append(out, "--cert-file", cfg.certFile)
	}
	if cfg.keyFile != "" {
		out = append(out, "--key-file", cfg.keyFile)
	}
	return out
}

// runServeStop reads the PID file, sends SIGTERM to the process, and
// polls for exit. After 10s without exit, escalates to SIGKILL.
// Removes the PID file on success.
func runServeStop(args []string) error {
	fs := flag.NewFlagSet("serve stop", flag.ContinueOnError)
	pidPath := fs.String("pid-file", defaultPIDPath, "PID file location")
	timeout := fs.Duration("timeout", 10*time.Second, "wait this long for graceful exit before SIGKILL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolved, err := expandHome(*pidPath)
	if err != nil {
		return fmt.Errorf("resolve pid path: %w", err)
	}

	pid, err := readPIDFile(resolved)
	if err != nil {
		return fmt.Errorf("read pid file: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			_ = os.Remove(resolved) //nolint:errcheck
			fmt.Printf("not running (pid %d already gone); cleaned pidfile\n", pid)
			return nil
		}
		return fmt.Errorf("send SIGTERM to pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			_ = os.Remove(resolved) //nolint:errcheck
			fmt.Printf("stopped bridge (pid %d)\n", pid)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Escalate.
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("send SIGKILL to pid %d: %w", pid, err)
	}
	_ = os.Remove(resolved) //nolint:errcheck
	fmt.Printf("killed bridge (pid %d) after %s with no graceful exit\n", pid, *timeout)
	return nil
}

// runServeRestart stops the running daemon (if any) and starts a new
// one with the same arguments.
func runServeRestart(args []string) error {
	// Find --pid-file (if any) by scanning args directly. We can't
	// use a flag.FlagSet here because args may include start-only
	// flags (--port, --db, …) that a narrow set would reject.
	pidPath := extractFlag(args, "pid-file", defaultPIDPath)
	_ = runServeStop([]string{"--pid-file", pidPath}) //nolint:errcheck // best-effort; start follows
	// Small settling time so the next Start sees a clean state.
	time.Sleep(200 * time.Millisecond)
	return runServeStart(args)
}

// extractFlag picks the value of --name from a freeform args slice,
// supporting both `--name=value` and `--name value` forms. Returns
// fallback when the flag is absent.
func extractFlag(args []string, name, fallback string) string {
	prefix := "--" + name
	for i, a := range args {
		if a == prefix && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, prefix+"=") {
			return a[len(prefix)+1:]
		}
	}
	return fallback
}

// runServeStatus reports whether the daemon is running, based on the
// PID file plus a liveness probe. Always exits 0 — status is
// informational, not a gate.
func runServeStatus(args []string) error {
	fs := flag.NewFlagSet("serve status", flag.ContinueOnError)
	pidPath := fs.String("pid-file", defaultPIDPath, "PID file location")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolved, err := expandHome(*pidPath)
	if err != nil {
		return err
	}

	pid, err := readPIDFile(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("not running (no pid file)")
			return nil
		}
		return err
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("not running (pid %d not found)\n", pid)
		return nil
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		fmt.Printf("not running (pid %d not alive); pidfile is stale\n", pid)
		return nil
	}
	fmt.Printf("running (pid %d)\n", pid)
	return nil
}

// readLivePID returns the PID stored in path and whether the process
// is alive. (-1, false) when the PID file is missing or the process
// is gone.
func readLivePID(path string) (int, bool) {
	pid, err := readPIDFile(path)
	if err != nil {
		return -1, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return pid, false
	}
	return pid, true
}

// readPIDFile returns the PID stored at path. Returns os.ErrNotExist
// (wrapped) when the file is missing; a generic error on parse failure.
func readPIDFile(path string) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied PID file path
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("parse pid file %s: %w", path, err)
	}
	return pid, nil
}

// --- helpers shared with certs.go ------------------------------------------

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func loadSchemasDir(store *messagestore.Store, dir string) (int, error) {
	resolved, err := expandHome(dir)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", resolved, err)
	}

	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		typeName := strings.TrimSuffix(name, ".json")
		path := filepath.Join(resolved, name)
		raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied schemas dir
		if err != nil {
			return n, fmt.Errorf("read %s: %w", path, err)
		}
		sch, err := schema.Parse(json.RawMessage(raw))
		if err != nil {
			return n, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := store.RegisterType(messagestore.MessageType{
			Name:   typeName,
			Schema: sch,
		}); err != nil {
			return n, fmt.Errorf("register %s: %w", typeName, err)
		}
		n++
	}
	return n, nil
}
