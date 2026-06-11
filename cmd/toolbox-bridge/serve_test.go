package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractFlag: Go's flag package treats -pid-file and --pid-file
// identically, and the rest of the CLI documents single-dash spellings —
// extractFlag must accept both, or `serve restart -pid-file X` stops the
// daemon recorded at the DEFAULT pidfile (possibly a different instance)
// while starting the new one at X.
func TestExtractFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"double dash separate", []string{"--pid-file", "/tmp/x.pid"}, "/tmp/x.pid"},
		{"double dash equals", []string{"--pid-file=/tmp/y.pid"}, "/tmp/y.pid"},
		{"single dash separate", []string{"-pid-file", "/tmp/z.pid"}, "/tmp/z.pid"},
		{"single dash equals", []string{"-pid-file=/tmp/w.pid"}, "/tmp/w.pid"},
		{"absent", []string{"--port", "1"}, defaultPIDPath},
		{"mixed with other flags", []string{"--port", "1", "-pid-file", "/tmp/v.pid", "--no-tls"}, "/tmp/v.pid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractFlag(tc.args, "pid-file", defaultPIDPath)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestLooksLikeBridge pins the identity predicate used by the PID
// liveness checks.
func TestLooksLikeBridge(t *testing.T) {
	t.Parallel()
	assert.True(t, looksLikeBridge("/Users/x/go/bin/toolbox-bridge serve run --db /tmp/m.db"))
	assert.True(t, looksLikeBridge("toolbox-bridge serve run"))
	assert.False(t, looksLikeBridge("/usr/bin/vim main.go"))
	assert.False(t, looksLikeBridge("sleep 60"))
	assert.False(t, looksLikeBridge(""))
}

// TestReadLivePID_RejectsRecycledPID: the pidfile lives under
// ~/.toolbox/run, which survives reboots — after a reboot the stored PID
// is likely recycled to an unrelated same-user process, Signal(0)
// succeeds, and `serve stop` (which doctor actively recommends in this
// state) SIGTERMs an innocent process. Liveness must verify identity,
// not just signalability. A spawned `sleep` stands in for the innocent
// process.
func TestReadLivePID_RejectsRecycledPID(t *testing.T) {
	t.Parallel()

	sleep := exec.Command("sleep", "60")
	require.NoError(t, sleep.Start())
	t.Cleanup(func() {
		_ = sleep.Process.Kill()
		_, _ = sleep.Process.Wait()
	})

	path := filepath.Join(t.TempDir(), "bridge.pid")
	require.NoError(t, os.WriteFile(path,
		[]byte(strconv.Itoa(sleep.Process.Pid)+"\n"), 0o600))

	_, alive := readLivePID(path)
	assert.False(t, alive,
		"a live process that is not toolbox-bridge must not be reported as the running bridge")
}
