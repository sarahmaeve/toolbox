package pdf

import (
	"testing"
	"time"
)

// pageTreeFixture builds a pdfFile whose page tree lives entirely in the
// resolve cache: object 1 is the catalog, object 2 the root /Pages node.
// Callers add further nodes to f.cache directly.
func pageTreeFixture() *pdfFile {
	f := newTestPDFFile()
	f.trailer = pdfDict{"Root": pdfRef{num: 1}}
	f.cache[1] = pdfDict{"Type": pdfName("Catalog"), "Pages": pdfRef{num: 2}}
	return f
}

// TestGetPages_BreaksKidsSelfReferenceCycle: a /Pages node whose /Kids
// contains itself must terminate instead of recursing until the
// goroutine stack limit. Unlike resolve's inFlight cycles, page-tree
// recursion happens on cached dicts, so the overflow is a fatal runtime
// error recoverAsError cannot catch — the process dies.
func TestGetPages_BreaksKidsSelfReferenceCycle(t *testing.T) {
	t.Parallel()

	f := pageTreeFixture()
	f.cache[2] = pdfDict{"Type": pdfName("Pages"), "Kids": pdfArray{pdfRef{num: 2}}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.getPages()
	}()
	select {
	case <-done:
		// Cycle broken — surviving is the property under test.
	case <-time.After(2 * time.Second):
		t.Fatal("getPages did not return within 2s — /Kids self-reference cycle not broken")
	}
}

// TestGetPages_DuplicateKidsCollectedOnce: a non-cyclic tree where every
// /Pages node lists the same kid twice gives 2^depth leaf visits unless
// each node is walked once. 40 levels is enough to outlive any timeout
// if the walk is exponential, while finishing instantly when deduplicated.
// Legitimate trees never share nodes (a page has exactly one /Parent),
// so collecting each ref once is also the correct extraction semantic.
func TestGetPages_DuplicateKidsCollectedOnce(t *testing.T) {
	t.Parallel()

	f := pageTreeFixture()
	const levels = 40
	// Nodes 2..41 are /Pages, each listing node n+1 twice; node 42 is the leaf.
	for n := 2; n < 2+levels; n++ {
		f.cache[n] = pdfDict{
			"Type": pdfName("Pages"),
			"Kids": pdfArray{pdfRef{num: n + 1}, pdfRef{num: n + 1}},
		}
	}
	leaf := 2 + levels
	f.cache[leaf] = pdfDict{"Type": pdfName("Page")}

	type result struct {
		refs []pdfRef
		err  error
	}
	done := make(chan result, 1)
	go func() {
		refs, err := f.getPages()
		done <- result{refs, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("getPages: %v", got.err)
		}
		if len(got.refs) != 1 {
			t.Errorf("got %d page refs, want 1 (leaf collected exactly once)", len(got.refs))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("getPages did not return within 2s — duplicate /Kids walk is exponential")
	}
}

// TestGetPages_DeepLinearChainCapped: a linear chain of /Pages nodes
// deeper than any legitimate document must be cut off rather than walked
// to arbitrary depth — a 100 MB file can define millions of chained
// nodes, enough to exhaust the 1 GB goroutine stack ceiling even without
// a cycle. Pages beyond the cap are dropped, so the leaf at the bottom
// of a 10000-deep chain must not be collected.
func TestGetPages_DeepLinearChainCapped(t *testing.T) {
	t.Parallel()

	f := pageTreeFixture()
	const levels = 10000
	for n := 2; n < 2+levels; n++ {
		f.cache[n] = pdfDict{
			"Type": pdfName("Pages"),
			"Kids": pdfArray{pdfRef{num: n + 1}},
		}
	}
	leaf := 2 + levels
	f.cache[leaf] = pdfDict{"Type": pdfName("Page")}

	refs, err := f.getPages()
	if err != nil {
		t.Fatalf("getPages: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("got %d page refs, want 0 (chain deeper than the cap must be truncated)", len(refs))
	}
}
