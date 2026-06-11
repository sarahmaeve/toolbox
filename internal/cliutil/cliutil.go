// Package cliutil holds small input-handling helpers shared by the
// toolbox binaries and packages: home-directory expansion,
// comma-separated flag splitting, and page-range parsing. Extracted
// from near-verbatim copies in cmd/toolbox-bridge, cmd/toolbox-mcp,
// cmd/toolbox-pdf, and pkg/certs.
package cliutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExpandHome resolves a leading `~/` or bare `~` to the user's home
// directory. Returns the input unchanged when it doesn't start with
// `~`. `~otheruser/...` is not supported and passes through unchanged.
func ExpandHome(p string) (string, error) {
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
	return p, nil
}

// SplitCSV splits a comma-separated flag value into trimmed, non-empty
// elements. Returns nil for the empty string.
func SplitCSV(s string) []string {
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

// ParsePageRange validates and resolves page / pages selector inputs
// into an inclusive 1-indexed (from, to) range. (0, 0) means "all
// pages". page takes precedence when both are set; callers declare the
// two mutually exclusive at the flag/schema level.
func ParsePageRange(page int, pages string) (int, int, error) {
	switch {
	case page != 0:
		if page < 1 {
			return 0, 0, fmt.Errorf("invalid page %d (must be >= 1)", page)
		}
		return page, page, nil
	case pages != "":
		parts := strings.SplitN(pages, "-", 2)
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("invalid pages %q (expected N-M)", pages)
		}
		from, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid pages start %q: %w", parts[0], err)
		}
		to, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid pages end %q: %w", parts[1], err)
		}
		if from < 1 || to < from {
			return 0, 0, fmt.Errorf("invalid pages range %d-%d", from, to)
		}
		return from, to, nil
	default:
		return 0, 0, nil
	}
}
