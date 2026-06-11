package cliutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandHome(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain path untouched", "/etc/hosts", "/etc/hosts"},
		{"relative path untouched", "foo/bar", "foo/bar"},
		{"bare tilde", "~", home},
		{"tilde slash", "~/.toolbox/messages.db", filepath.Join(home, ".toolbox/messages.db")},
		{"other-user tilde passes through", "~root/x", "~root/x"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ExpandHome(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSplitCSV(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty is nil", "", nil},
		{"single", "agent", []string{"agent"}},
		{"multiple", "agent,orchestrator,user", []string{"agent", "orchestrator", "user"}},
		{"whitespace trimmed", " agent , user ", []string{"agent", "user"}},
		{"empty elements dropped", "agent,,user,", []string{"agent", "user"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, SplitCSV(tc.in))
		})
	}
}

// TestParsePageRange covers every documented mode of the page/pages
// selector inputs. Reached from toolbox-pdf's dump/images flags and
// toolbox-mcp's pdf_extract_text / pdf_extract_images tools — the only
// place those inputs are normalized; silently accepting a bad range
// would cascade into a pkg/pdf call against a nonsense page list.
func TestParsePageRange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		page     int
		pages    string
		wantFrom int
		wantTo   int
		wantErr  bool
	}{
		// Happy paths.
		{name: "both empty means all pages", wantFrom: 0, wantTo: 0},
		{name: "single page", page: 5, wantFrom: 5, wantTo: 5},
		{name: "explicit range", pages: "3-7", wantFrom: 3, wantTo: 7},
		{name: "single-page range", pages: "4-4", wantFrom: 4, wantTo: 4},
		// Page is preferred when both are set — pin the precedence so a
		// future refactor doesn't silently flip it (callers declare the
		// two mutually exclusive, but this helper accepts both shapes).
		{name: "page wins when both set", page: 2, pages: "10-20", wantFrom: 2, wantTo: 2},

		// page= negative / zero edge.
		{name: "negative page rejected", page: -1, wantErr: true},

		// pages= shape errors.
		{name: "pages without dash", pages: "5", wantErr: true},
		{name: "pages leading dash", pages: "-5", wantErr: true},
		{name: "pages trailing dash", pages: "5-", wantErr: true},
		{name: "pages non-numeric start", pages: "a-5", wantErr: true},
		{name: "pages non-numeric end", pages: "5-z", wantErr: true},
		{name: "pages decimal start", pages: "1.5-5", wantErr: true},

		// pages= range semantics.
		{name: "pages from below 1", pages: "0-5", wantErr: true},
		{name: "pages reversed range", pages: "10-2", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			from, to, err := ParsePageRange(tc.page, tc.pages)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got (%d, %d)", from, to)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if from != tc.wantFrom || to != tc.wantTo {
				t.Errorf("range: got (%d, %d), want (%d, %d)",
					from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}
