package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sarahmaeve/toolbox/pkg/certs"
	"github.com/sarahmaeve/toolbox/pkg/messagestore"
)

//go:embed example-schemas/*.json
var embeddedSchemas embed.FS

// runInit performs the new-user bootstrap: creates the standard
// ~/.toolbox layout, copies the mkcert CA into the managed path,
// generates the server cert (using an already-installed mkcert),
// optionally seeds example schemas, optionally patches the shell
// profile, and applies database migrations so the first `serve` is
// fast.
//
// Anything that requires installing a tool (mkcert, mkcert -install)
// surfaces as an error with the install hint — init never runs an
// installer itself.
func runInit(args []string) error {
	fset := flag.NewFlagSet("init", flag.ContinueOnError)
	writeProfile := fset.Bool("write-profile", false, "append the env-var export to ~/.zshrc")
	seedSchemas := fset.Bool("seed-schemas", false, "copy the example schemas into ~/.toolbox/schemas/")
	skipServerCert := fset.Bool("skip-server-cert", false, "skip the mkcert 127.0.0.1 localhost step (you must produce server certs another way before `serve start`)")
	if err := fset.Parse(args); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve $HOME: %w", err)
	}
	toolboxDir := filepath.Join(home, ".toolbox")

	// 1. Create the standard layout.
	for _, sub := range []string{"certs", "run", "log", "schemas"} {
		p := filepath.Join(toolboxDir, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", p, err)
		}
	}
	fmt.Printf("ensured %s/{certs,run,log,schemas}\n", toolboxDir)

	// 2. Verify mkcert is present.
	mkcertBin, err := exec.LookPath("mkcert")
	if err != nil {
		return errors.New("mkcert is not on PATH.\n\n" +
			"  Install it with one of:\n" +
			"    brew install mkcert       (macOS)\n" +
			"    sudo apt install mkcert   (Debian/Ubuntu)\n" +
			"    sudo pacman -S mkcert     (Arch)\n\n" +
			"  Then run `mkcert -install` once to wire your system trust store,\n" +
			"  then re-run `toolbox-bridge init`.")
	}

	// 3. Verify mkcert -install has been run (CAROOT contains a
	// rootCA.pem). We don't run mkcert -install ourselves — that
	// mutates the system trust store and is the user's call.
	rootCAPath, err := mkcertRootCAPath(mkcertBin)
	if err != nil {
		return err
	}
	fmt.Printf("mkcert CA at %s\n", rootCAPath)

	// 4. Copy CA into the managed path.
	mgr := certs.New(certs.Config{EnvVar: EnvVar, CertDir: CertDir})
	result, err := mgr.Init(certs.InitOptions{Stderr: os.Stderr})
	if err != nil {
		return fmt.Errorf("certs init: %w", err)
	}
	for _, a := range result.Actions {
		fmt.Println(a)
	}

	// 5. Generate the server cert if absent.
	if !*skipServerCert {
		if err := ensureServerCert(mkcertBin, filepath.Join(toolboxDir, "certs")); err != nil {
			return fmt.Errorf("generate server cert: %w", err)
		}
	}

	// 6. Optionally patch the shell profile.
	if *writeProfile {
		pr, err := mgr.WriteProfile(certs.WriteProfileOptions{CAPath: result.CAPath})
		if err != nil {
			return fmt.Errorf("write profile: %w", err)
		}
		fmt.Printf("profile %s: %s\n", pr.ProfilePath, pr.Action)
	}

	// 7. Optionally seed example schemas.
	if *seedSchemas {
		n, err := seedEmbeddedSchemas(filepath.Join(toolboxDir, "schemas"))
		if err != nil {
			return fmt.Errorf("seed schemas: %w", err)
		}
		fmt.Printf("seeded %d example schemas into %s/schemas/\n", n, toolboxDir)
	}

	// 8. Open the DB once to apply migrations.
	dbPath := filepath.Join(toolboxDir, "messages.db")
	store, err := messagestore.Open(context.Background(), messagestore.Config{DBPath: dbPath})
	if err != nil {
		return fmt.Errorf("open db %s: %w", dbPath, err)
	}
	_ = store.Close() //nolint:errcheck
	fmt.Printf("migrated database at %s\n", dbPath)

	// 9. Print the next step.
	fmt.Println("\ninit complete.")
	if *writeProfile {
		fmt.Printf("Next: run `source ~/.zshrc` (or restart your terminal) to pick up %s,\n", EnvVar)
		fmt.Println("      then `toolbox-bridge serve start --schemas-dir ~/.toolbox/schemas`.")
	} else {
		fmt.Printf("Next: export %s=%s in your shell\n", EnvVar, result.CAPath)
		fmt.Println("      (or re-run with --write-profile),")
		fmt.Println("      then `toolbox-bridge serve start --schemas-dir ~/.toolbox/schemas`.")
	}
	return nil
}

// mkcertRootCAPath returns the absolute path to the mkcert rootCA.pem
// or an error with an install hint if mkcert hasn't been initialized.
func mkcertRootCAPath(mkcertBin string) (string, error) {
	out, err := exec.Command(mkcertBin, "-CAROOT").Output() //nolint:gosec // G204: mkcertBin from exec.LookPath
	if err != nil {
		return "", fmt.Errorf("run `mkcert -CAROOT`: %w", err)
	}
	caDir := strings.TrimSpace(string(out))
	if caDir == "" {
		return "", errors.New("mkcert -CAROOT returned empty output")
	}
	caPath := filepath.Join(caDir, "rootCA.pem")
	if _, err := os.Stat(caPath); err != nil {
		return "", fmt.Errorf("mkcert CA not found at %s.\n\n"+
			"  Run `mkcert -install` once to populate the system trust store.\n"+
			"  Then re-run `toolbox-bridge init`", caPath)
	}
	return caPath, nil
}

// ensureServerCert generates the 127.0.0.1+1 cert pair in certsDir if
// it isn't already there. Idempotent — running mkcert with output
// flags pointing at existing files would clobber them, so we check
// first.
func ensureServerCert(mkcertBin, certsDir string) error {
	certPath := filepath.Join(certsDir, "127.0.0.1+1.pem")
	keyPath := filepath.Join(certsDir, "127.0.0.1+1-key.pem")
	if statExists(certPath) && statExists(keyPath) {
		fmt.Printf("server cert already present at %s\n", certPath)
		return nil
	}
	cmd := exec.Command(mkcertBin, //nolint:gosec // G204: mkcertBin from exec.LookPath
		"-cert-file", certPath,
		"-key-file", keyPath,
		"127.0.0.1", "localhost")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mkcert 127.0.0.1 localhost: %w", err)
	}
	fmt.Printf("generated server cert at %s\n", certPath)
	return nil
}

func statExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// seedEmbeddedSchemas copies the embedded example schemas into dst.
// Files that already exist are skipped (don't clobber the user's own
// schemas of the same name).
func seedEmbeddedSchemas(dst string) (int, error) {
	entries, err := embeddedSchemas.ReadDir("example-schemas")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		target := filepath.Join(dst, e.Name())
		if statExists(target) {
			fmt.Printf("skipped %s (already exists)\n", target)
			continue
		}
		data, err := fs.ReadFile(embeddedSchemas, filepath.Join("example-schemas", e.Name()))
		if err != nil {
			return n, err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
