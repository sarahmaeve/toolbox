package certs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEnvVar = "TOOLBOX_TEST_CA_CERTS"

// newTestManager returns a Manager configured for tests. Each test
// gets its own CertDir under t.TempDir so they don't collide.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return New(Config{
		EnvVar:  testEnvVar,
		CertDir: t.TempDir(),
	})
}

// pemFixture is a minimal byte sequence pem.Decode accepts as
// "looks like a certificate."
func pemFixture(t *testing.T) []byte {
	t.Helper()
	return []byte(`-----BEGIN CERTIFICATE-----
MIIBkTCB+wIJAKH/EhuqJjtGMA0GCSqGSIb3DQEBCwUAMBoxGDAWBgNVBAMMD3Rl
c3Qtc2lnbmF0b3J5LWNhMB4XDTI2MDQyMjAwMDAwMFoXDTM2MDQyMjAwMDAwMFow
GjEYMBYGA1UEAwwPdGVzdC1zaWduYXRvcnktY2EwgZ8wDQYJKoZIhvcNAQEBBQAD
gY0AMIGJAoGBAM3fake3fake3fake3fake3fake3fake3fake3fake3fake3fake
-----END CERTIFICATE-----
`)
}

// --- Config defaults -------------------------------------------------------

func TestNew_PanicsOnMissingEnvVar(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { New(Config{}) },
		"empty EnvVar is a programmer error caught at construction")
}

func TestNew_AppliesDefaults(t *testing.T) {
	t.Parallel()
	m := New(Config{EnvVar: "FOO_CA"})
	cfg := m.Config()
	assert.Equal(t, "FOO_CA", cfg.EnvVar)
	assert.Equal(t, "~/.toolbox/certs", cfg.CertDir)
	assert.Equal(t, "rootCA.pem", cfg.CAFileName)
	assert.Equal(t, "~/.zshrc", cfg.ShellProfile)
	assert.Contains(t, cfg.ProfileMarkerBegin, "FOO_CA")
	assert.Contains(t, cfg.ProfileMarkerEnd, "FOO_CA")
}

// --- Check: preflight ------------------------------------------------------

func TestCheck_EnvUnset(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	r := m.CheckWithEnv(func(string) string { return "" })
	assert.False(t, r.OK)
	assert.Equal(t, FailEnvUnset, r.Code)
	assert.Contains(t, r.Message, testEnvVar)
	assert.NotEmpty(t, r.Fix)
}

func TestCheck_PathMissing(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	r := m.CheckWithEnv(func(string) string { return "/definitely/not/real/rootCA.pem" })
	assert.False(t, r.OK)
	assert.Equal(t, FailPathMissing, r.Code)
	assert.Contains(t, r.Message, "/definitely/not/real/rootCA.pem")
}

func TestCheck_NotAPEM(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	path := filepath.Join(t.TempDir(), "rootCA.pem")
	require.NoError(t, os.WriteFile(path, []byte("this is not a pem file"), 0o600))

	r := m.CheckWithEnv(func(string) string { return path })
	assert.False(t, r.OK)
	assert.Equal(t, FailPathInvalid, r.Code)
}

func TestCheck_Valid(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	path := filepath.Join(t.TempDir(), "rootCA.pem")
	require.NoError(t, os.WriteFile(path, pemFixture(t), 0o600))

	r := m.CheckWithEnv(func(string) string { return path })
	assert.True(t, r.OK)
	assert.Equal(t, StatusOK, r.Code)
	assert.Empty(t, r.Fix)
}

func TestCheck_ExpandsTilde(t *testing.T) {
	m := newTestManager(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "rootCA.pem")
	require.NoError(t, os.WriteFile(path, pemFixture(t), 0o600))

	r := m.CheckWithEnv(func(string) string { return "~/rootCA.pem" })
	assert.True(t, r.OK)
}

// --- Init -------------------------------------------------------------------

func TestInit_CopiesCAFromMkcert(t *testing.T) {
	certDir := t.TempDir()
	m := New(Config{EnvVar: testEnvVar, CertDir: certDir})

	fixture := filepath.Join(t.TempDir(), "rootCA.pem")
	require.NoError(t, os.WriteFile(fixture, pemFixture(t), 0o600))

	restore := setMkcertCAForTest(func() (string, error) { return fixture, nil })
	t.Cleanup(restore)

	result, err := m.Init(InitOptions{Stderr: io.Discard})
	require.NoError(t, err)

	assert.Equal(t, certDir, result.CertDir)
	assert.Equal(t, filepath.Join(certDir, "rootCA.pem"), result.CAPath)

	got, err := os.ReadFile(result.CAPath)
	require.NoError(t, err)
	want, err := os.ReadFile(fixture)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestInit_Idempotent(t *testing.T) {
	certDir := t.TempDir()
	m := New(Config{EnvVar: testEnvVar, CertDir: certDir})

	fixture := filepath.Join(t.TempDir(), "rootCA.pem")
	require.NoError(t, os.WriteFile(fixture, pemFixture(t), 0o600))
	restore := setMkcertCAForTest(func() (string, error) { return fixture, nil })
	t.Cleanup(restore)

	_, err := m.Init(InitOptions{Stderr: io.Discard})
	require.NoError(t, err)
	_, err = m.Init(InitOptions{Stderr: io.Discard})
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(certDir, "rootCA.pem"))
	assert.NoError(t, statErr)
}

func TestInit_RefreshesStaleCAOnRerun(t *testing.T) {
	certDir := t.TempDir()
	m := New(Config{EnvVar: testEnvVar, CertDir: certDir})

	fixture := filepath.Join(t.TempDir(), "rootCA.pem")
	require.NoError(t, os.WriteFile(fixture, pemFixture(t), 0o600))
	restore := setMkcertCAForTest(func() (string, error) { return fixture, nil })
	t.Cleanup(restore)

	_, err := m.Init(InitOptions{Stderr: io.Discard})
	require.NoError(t, err)

	newBytes := append(pemFixture(t), []byte("# rotated\n")...)
	require.NoError(t, os.WriteFile(fixture, newBytes, 0o600))

	_, err = m.Init(InitOptions{Stderr: io.Discard})
	require.NoError(t, err)

	got, _ := os.ReadFile(filepath.Join(certDir, "rootCA.pem"))
	assert.Equal(t, newBytes, got)
}

func TestInit_MkcertNotFound(t *testing.T) {
	m := newTestManager(t)
	restore := setMkcertCAForTest(func() (string, error) { return "", ErrMkcertNotFound })
	t.Cleanup(restore)

	_, err := m.Init(InitOptions{Stderr: io.Discard})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMkcertNotFound))
}

func TestInit_CreatesCertDirWhenMissing(t *testing.T) {
	root := t.TempDir()
	certDir := filepath.Join(root, "nested", "does", "not", "exist")
	m := New(Config{EnvVar: testEnvVar, CertDir: certDir})

	fixture := filepath.Join(t.TempDir(), "rootCA.pem")
	require.NoError(t, os.WriteFile(fixture, pemFixture(t), 0o600))
	restore := setMkcertCAForTest(func() (string, error) { return fixture, nil })
	t.Cleanup(restore)

	_, err := m.Init(InitOptions{Stderr: io.Discard})
	require.NoError(t, err)

	info, err := os.Stat(certDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

// --- WriteProfile -----------------------------------------------------------

func TestWriteProfile_AppendsWhenMissing(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	profile := filepath.Join(t.TempDir(), ".zshrc")
	original := "export PATH=/foo:$PATH\nalias ll='ls -la'\n"
	require.NoError(t, os.WriteFile(profile, []byte(original), 0o600))

	result, err := m.WriteProfile(WriteProfileOptions{
		ProfilePath: profile,
		CAPath:      "/Users/sarah/.toolbox/certs/rootCA.pem",
	})
	require.NoError(t, err)
	assert.Equal(t, ProfileAppended, result.Action)

	got := readFile(t, profile)
	assert.Contains(t, got, original)
	assert.Contains(t, got, m.cfg.ProfileMarkerBegin)
	assert.Contains(t, got, m.cfg.ProfileMarkerEnd)
	assert.Contains(t, got,
		`export `+testEnvVar+`="/Users/sarah/.toolbox/certs/rootCA.pem"`)
}

func TestWriteProfile_ReplacesExistingBlock(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	profile := filepath.Join(t.TempDir(), ".zshrc")
	initial := "line-before\n" +
		m.cfg.ProfileMarkerBegin + "\n" +
		`export ` + testEnvVar + `="/old/path/rootCA.pem"` + "\n" +
		m.cfg.ProfileMarkerEnd + "\n" +
		"line-after\n"
	require.NoError(t, os.WriteFile(profile, []byte(initial), 0o600))

	result, err := m.WriteProfile(WriteProfileOptions{
		ProfilePath: profile,
		CAPath:      "/new/path/rootCA.pem",
	})
	require.NoError(t, err)
	assert.Equal(t, ProfileReplaced, result.Action)

	got := readFile(t, profile)
	assert.Contains(t, got, "line-before")
	assert.Contains(t, got, "line-after")
	assert.Contains(t, got, "/new/path/rootCA.pem")
	assert.NotContains(t, got, "/old/path/rootCA.pem")
	assert.Equal(t, 1, strings.Count(got, m.cfg.ProfileMarkerBegin))
	assert.Equal(t, 1, strings.Count(got, m.cfg.ProfileMarkerEnd))
}

func TestWriteProfile_UnchangedWhenIdentical(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	profile := filepath.Join(t.TempDir(), ".zshrc")

	_, err := m.WriteProfile(WriteProfileOptions{ProfilePath: profile, CAPath: "/p/rootCA.pem"})
	require.NoError(t, err)

	r2, err := m.WriteProfile(WriteProfileOptions{ProfilePath: profile, CAPath: "/p/rootCA.pem"})
	require.NoError(t, err)
	assert.Equal(t, ProfileUnchanged, r2.Action)
}

func TestWriteProfile_CreatesFileWhenMissing(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	profile := filepath.Join(t.TempDir(), ".zshrc")

	result, err := m.WriteProfile(WriteProfileOptions{
		ProfilePath: profile,
		CAPath:      "/p/rootCA.pem",
	})
	require.NoError(t, err)
	assert.Equal(t, ProfileCreated, result.Action)

	got := readFile(t, profile)
	assert.Contains(t, got, m.cfg.ProfileMarkerBegin)
	assert.Contains(t, got, "/p/rootCA.pem")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
