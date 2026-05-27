package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/sarahmaeve/toolbox/pkg/certs"
)

// runCerts dispatches the `certs` subcommand to init or check.
func runCerts(args []string) error {
	if len(args) == 0 {
		return errors.New("certs requires a subcommand: init | check")
	}
	sub := args[0]
	rest := args[1:]
	m := certs.New(certs.Config{
		EnvVar:  EnvVar,
		CertDir: CertDir,
	})
	switch sub {
	case "init":
		return runCertsInit(m, rest)
	case "check":
		return runCertsCheck(m, rest)
	case "-h", "--help", "help":
		fmt.Println("certs: init | check")
		return nil
	default:
		return fmt.Errorf("unknown certs subcommand: %s", sub)
	}
}

func runCertsInit(m *certs.Manager, args []string) error {
	fs := flag.NewFlagSet("certs init", flag.ContinueOnError)
	writeProfile := fs.Bool("write-profile", false, "append the env-var export to ~/.zshrc")
	if err := fs.Parse(args); err != nil {
		return err
	}

	result, err := m.Init(certs.InitOptions{Stderr: os.Stderr})
	if err != nil {
		return err
	}
	for _, action := range result.Actions {
		fmt.Println(action)
	}

	if *writeProfile {
		pr, err := m.WriteProfile(certs.WriteProfileOptions{CAPath: result.CAPath})
		if err != nil {
			return err
		}
		fmt.Printf("profile %s: %s\n", pr.ProfilePath, pr.Action)
		fmt.Printf("\nDone. Run `source %s` (or restart your terminal) to pick up %s.\n",
			pr.ProfilePath, EnvVar)
	} else {
		fmt.Printf("\nManaged CA at %s\n", result.CAPath)
		fmt.Printf("Export %s=%s in your shell (or re-run with --write-profile)\n",
			EnvVar, result.CAPath)
	}
	return nil
}

func runCertsCheck(m *certs.Manager, _ []string) error {
	r := m.Check()
	if r.OK {
		fmt.Println("OK:", r.Message)
		return nil
	}
	fmt.Println("FAIL:", r.Message)
	if r.Fix != "" {
		fmt.Println("FIX: ", r.Fix)
	}
	return errors.New("certs check failed")
}
