package certs

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// WriteProfileOptions drives WriteProfile. CAPath is the value the env
// var should be set to — typically the stable path returned by
// InitResult.CAPath.
type WriteProfileOptions struct {
	// ProfilePath overrides the Manager's configured ShellProfile.
	// Useful for tests and for explicit operator overrides. Empty uses
	// the Manager's configured path.
	ProfilePath string
	CAPath      string
}

// WriteProfileResult reports what WriteProfile did.
type WriteProfileResult struct {
	ProfilePath string
	Action      ProfileAction
}

// WriteProfile appends or updates the managed block in the user's
// shell profile. The block is bracketed with the configured markers
// so re-running replaces it atomically — no duplicate exports, no
// stale values left behind.
//
// Writes via temp-file + rename for crash safety. An interrupted
// write leaves the previous profile content intact rather than a
// half-written dotfile.
//
// Idempotency contract:
//
//   - First run on a profile without the block: ProfileAppended.
//   - Profile doesn't exist yet: ProfileCreated.
//   - Subsequent run, same CAPath: ProfileUnchanged (no disk write).
//   - Subsequent run, different CAPath: ProfileReplaced.
func (m *Manager) WriteProfile(opts WriteProfileOptions) (*WriteProfileResult, error) {
	if opts.CAPath == "" {
		return nil, errors.New("WriteProfile: CAPath must be non-empty")
	}
	profilePath := opts.ProfilePath
	if profilePath == "" {
		profilePath = m.cfg.ShellProfile
	}
	resolved, err := expandHome(profilePath)
	if err != nil {
		return nil, fmt.Errorf("resolve profile path %q: %w", profilePath, err)
	}

	result := &WriteProfileResult{ProfilePath: resolved}

	existing, existed, err := readIfExists(resolved)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", resolved, err)
	}

	newBlock := m.renderManagedBlock(opts.CAPath)

	updated, action := m.updateProfileContent(existing, newBlock, existed)

	if action == ProfileUnchanged {
		result.Action = ProfileUnchanged
		return result, nil
	}

	if err := writeFileAtomic(resolved, []byte(updated), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", resolved, err)
	}
	result.Action = action
	return result, nil
}

// renderManagedBlock returns the exact text (including trailing
// newline) that should live between the begin/end markers.
func (m *Manager) renderManagedBlock(caPath string) string {
	return fmt.Sprintf("%s\nexport %s=%q\n%s\n",
		m.cfg.ProfileMarkerBegin, m.cfg.EnvVar, caPath, m.cfg.ProfileMarkerEnd)
}

// updateProfileContent computes the new profile contents and the
// action taken. Pure function — no I/O — so it's cheap to unit-test
// and easy to reason about.
func (m *Manager) updateProfileContent(existing, newBlock string, fileExisted bool) (string, ProfileAction) {
	begin := strings.Index(existing, m.cfg.ProfileMarkerBegin)
	if begin < 0 {
		if !fileExisted {
			return newBlock, ProfileCreated
		}
		if existing == "" {
			return newBlock, ProfileAppended
		}
		sep := ""
		if !strings.HasSuffix(existing, "\n") {
			sep = "\n"
		}
		return existing + sep + newBlock, ProfileAppended
	}

	endIdx := strings.Index(existing[begin:], m.cfg.ProfileMarkerEnd)
	if endIdx < 0 {
		// Malformed — begin present but no end. Overwrite from begin
		// to EOF with the new block.
		return existing[:begin] + newBlock, ProfileReplaced
	}
	endIdx += begin + len(m.cfg.ProfileMarkerEnd)

	consumeEnd := endIdx
	if consumeEnd < len(existing) && existing[consumeEnd] == '\n' {
		consumeEnd++
	}

	before := existing[:begin]
	after := existing[consumeEnd:]
	replaced := before + newBlock + after

	if replaced == existing {
		return existing, ProfileUnchanged
	}
	return replaced, ProfileReplaced
}

func readIfExists(path string) (content string, existed bool, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: profile path is operator-supplied via Manager config
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".toolbox.tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
