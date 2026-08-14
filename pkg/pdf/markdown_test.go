package pdf

import (
	"strings"
	"testing"
)

func TestRenderPageMarkdown_PreservesLayoutSignals(t *testing.T) {
	t.Parallel()

	runs := []positionedTextRun{
		{text: "RUNNING HEADER", x: 80, y: 560, fontSize: 7},
		{text: "Section heading", x: 30, y: 520, fontSize: 10, bold: true},
		{text: "A ", x: 50, y: 500, fontSize: 10},
		{text: "foreign term", x: 62, y: 500, fontSize: 10, italic: true},
		{text: "7", x: 130, y: 504.5, fontSize: 7},
		{text: "contin-", x: 30, y: 488, fontSize: 10},
		{text: "ues with H", x: 30, y: 476, fontSize: 10},
		{text: "2", x: 78, y: 471.5, fontSize: 7},
		{text: "O.", x: 84, y: 476, fontSize: 10},
		{text: "7", x: 35, y: 124, fontSize: 8},
		{text: "Citation text.", x: 45, y: 120, fontSize: 8},
	}

	got := renderPageMarkdown(42, 400, 600, runs)
	for _, want := range []string{
		"<!-- PDF page 42 -->",
		"## **Section heading**",
		"*foreign term*",
		"<sup>7</sup>",
		"continues with H<sub>2</sub>O.",
		"<sup>7</sup> Citation text.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Markdown output does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "RUNNING HEADER") {
		t.Errorf("Markdown output retained running header:\n%s", got)
	}
}

func TestExtractPositionedText_UsesFontMetadataAndCoordinates(t *testing.T) {
	t.Parallel()

	content := []byte(`BT
/FI 11 Tf
1 0 0 1 24 72 Tm
(styled) Tj
ET`)
	fonts := map[string]pageFont{
		"FI": {cmap: buildSingleByteCMap("styled"), baseName: "Times-Italic", italic: true},
	}
	runs := extractPositionedText(content, fonts)
	if len(runs) != 1 {
		t.Fatalf("runs: got %d, want 1", len(runs))
	}
	got := runs[0]
	if got.text != "styled" || got.x != 24 || got.y != 72 || got.fontSize != 11 || !got.italic {
		t.Fatalf("run: got %+v", got)
	}
}

func TestExtractPositionedText_AppliesTextRise(t *testing.T) {
	t.Parallel()

	content := []byte(`BT
/F0 8 Tf
1 0 0 1 24 72 Tm
3 Ts
(7) Tj
ET`)
	runs := extractPositionedText(content, map[string]pageFont{"F0": {}})
	if len(runs) != 1 {
		t.Fatalf("runs: got %d, want 1", len(runs))
	}
	if runs[0].y != 75 {
		t.Fatalf("run y: got %v, want 75", runs[0].y)
	}
}

func TestMarkdownLayoutUsesInheritedPageAttributes(t *testing.T) {
	t.Parallel()

	f := newTestPDFFile()
	page := pdfDict{
		"Parent": pdfDict{
			"Resources": pdfDict{
				"Font": pdfDict{
					"F1": pdfDict{"BaseFont": pdfName("Example-Italic")},
				},
			},
			"MediaBox": pdfArray{pdfNumber(0), pdfNumber(0), pdfNumber(400), pdfNumber(600)},
		},
	}

	fonts := f.buildPageFonts(page)
	if font, ok := fonts["F1"]; !ok || !font.italic {
		t.Fatalf("inherited font: got %+v, want italic F1", font)
	}
	width, height := pageDimensions(page, f)
	if width != 400 || height != 600 {
		t.Fatalf("inherited page dimensions: got %vx%v, want 400x600", width, height)
	}
}

func TestSplitTrailingCitation_IsConservative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input      string
		wantPrefix string
		wantMarker string
		wantOK     bool
	}{
		{input: "elsewhere,T", wantPrefix: "elsewhere,", wantMarker: "T", wantOK: true},
		{input: "term.8", wantPrefix: "term.", wantMarker: "8", wantOK: true},
		{input: "name.r2", wantPrefix: "name.", wantMarker: "r2", wantOK: true},
		{input: "kitmd.n", wantPrefix: "kitmd.n", wantOK: false},
		{input: "majlīs.In", wantPrefix: "majlīs.In", wantOK: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			prefix, marker, ok := splitTrailingCitation(test.input)
			if prefix != test.wantPrefix || marker != test.wantMarker || ok != test.wantOK {
				t.Fatalf("splitTrailingCitation(%q) = (%q, %q, %v), want (%q, %q, %v)",
					test.input, prefix, marker, ok, test.wantPrefix, test.wantMarker, test.wantOK)
			}
		})
	}
}
