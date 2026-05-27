package pdfclean

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ordinal split by isolated hyphen rejoins",
			in:   "15\n-\nта окрема бригада",
			want: "<!-- page 1 -->\n\n15-та окрема бригада\n",
		},
		{
			name: "compound place name rejoins across multiple hyphens",
			in:   "Ростов\n-\nна\n-\nДону",
			want: "<!-- page 1 -->\n\nРостов-на-Дону\n",
		},
		{
			name: "product code with model number rejoins inside quotes",
			in:   "“Москва\n-\n1”",
			want: "<!-- page 1 -->\n\n“Москва-1”\n",
		},
		{
			name: "citation bracket marker rejoins",
			in:   "довідник [\n3\n\n] посилання",
			want: "<!-- page 1 -->\n\nдовідник [3] посилання\n",
		},
		{
			name: "figure caption with em-dash rejoins to single line",
			in:   "Рисунок 3\n\n–\n\nКомплекс РЕР",
			want: "<!-- page 1 -->\n\nРисунок 3 – Комплекс РЕР\n",
		},
		{
			name: "numeric range with en-dash rejoins without spaces",
			in:   "1,5\n–\n1,8 рази",
			want: "<!-- page 1 -->\n\n1,5–1,8 рази\n",
		},
		{
			name: "runs of blank lines collapse to single blank",
			in:   "Para one.\n\n\n\n\nPara two.",
			want: "<!-- page 1 -->\n\nPara one.\n\nPara two.\n",
		},
		{
			name: "form-feed page separator becomes page marker",
			in:   "End of page one.\n\f\nStart of page two.",
			want: "<!-- page 1 -->\n\nEnd of page one.\n\n<!-- page 2 -->\n\nStart of page two.\n",
		},
		{
			name: "multiple form-feeds increment page counter",
			in:   "A\n\f\nB\n\f\nC",
			want: "<!-- page 1 -->\n\nA\n\n<!-- page 2 -->\n\nB\n\n<!-- page 3 -->\n\nC\n",
		},
		{
			name: "real-world fragment combining hyphen and content",
			in:   "1084\n-\nй міжвидовий навчальний центр",
			want: "<!-- page 1 -->\n\n1084-й міжвидовий навчальний центр\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Clean(tc.in)
			if got != tc.want {
				t.Errorf("Clean(%q):\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLinkImages_EnglishFigureCaption(t *testing.T) {
	t.Parallel()

	markdown := "<!-- page 1 -->\n\nSome text.\n\n<!-- page 4 -->\n\n" +
		"Figure 4-2. Air guard observer scanning sectors.\n\nMore text.\n"

	manifest := map[int][]ImageRef{
		4: {{File: "atp-p0004-Im0.png", Name: "Im0"}},
	}

	got := LinkImages(markdown, manifest, "images/atp")
	want := "<!-- page 1 -->\n\nSome text.\n\n<!-- page 4 -->\n\n" +
		"![Figure 4-2](images/atp/atp-p0004-Im0.png)\n\n" +
		"Figure 4-2. Air guard observer scanning sectors.\n\nMore text.\n"

	if got != want {
		t.Errorf("linkImages:\n got: %q\nwant: %q", got, want)
	}
}

func TestLinkImages_UkrainianAbbrevRysCaption(t *testing.T) {
	t.Parallel()

	markdown := "<!-- page 9 -->\n\nРис. 2.1. Основні завдання РЕБ\n"
	manifest := map[int][]ImageRef{
		9: {{File: "pos-p0009-Image20.png", Name: "Image20"}},
	}

	got := LinkImages(markdown, manifest, "images/posibnyk")
	if !strings.Contains(got, "![Рис. 2.1](images/posibnyk/pos-p0009-Image20.png)") {
		t.Errorf("linkImages did not match Рис. abbreviation:\n%s", got)
	}
}

func TestLinkImages_UkrainianRysunokCaption(t *testing.T) {
	t.Parallel()

	markdown := "<!-- page 5 -->\n\nРисунок 1.2 – Структурна схема комплексу.\n"
	manifest := map[int][]ImageRef{
		5: {{File: "pos-p0005-Image20.png", Name: "Image20"}},
	}

	got := LinkImages(markdown, manifest, "images/posibnyk")
	if !strings.Contains(got, "![Рисунок 1.2](images/posibnyk/pos-p0005-Image20.png)") {
		t.Errorf("linkImages did not insert link; got:\n%s", got)
	}
	// Caption itself must be preserved verbatim.
	if !strings.Contains(got, "Рисунок 1.2 – Структурна схема комплексу.") {
		t.Errorf("linkImages dropped the caption text; got:\n%s", got)
	}
}

func TestLinkImages_NoCaptionNoLink(t *testing.T) {
	t.Parallel()

	// Page 4 has an image in the manifest but no caption in the text.
	// linkImages should not invent a link.
	markdown := "<!-- page 4 -->\n\nJust body text, no caption.\n"
	manifest := map[int][]ImageRef{
		4: {{File: "atp-p0004-Im0.png", Name: "Im0"}},
	}

	got := LinkImages(markdown, manifest, "images/atp")
	if strings.Contains(got, "![") {
		t.Errorf("linkImages emitted a link without a caption:\n%s", got)
	}
}

func TestLinkImages_BodyReferenceNotMatched(t *testing.T) {
	t.Parallel()

	// "see Figure 4-2" is a body reference, not a caption — must not link.
	markdown := "<!-- page 4 -->\n\nAs shown in Figure 4-2, observers scan in arcs.\n"
	manifest := map[int][]ImageRef{
		4: {{File: "atp-p0004-Im0.png", Name: "Im0"}},
	}

	got := LinkImages(markdown, manifest, "images/atp")
	if strings.Contains(got, "![") {
		t.Errorf("linkImages matched a body reference, not a caption:\n%s", got)
	}
}

func TestLinkImages_CaptionWithoutImage(t *testing.T) {
	t.Parallel()

	// Page 4 has a caption but no image in the manifest (e.g. the figure was
	// a JBIG2 we skipped). Caption is left alone — no broken link.
	markdown := "<!-- page 4 -->\n\nFigure 4-2. The thing.\n"
	manifest := map[int][]ImageRef{}

	got := LinkImages(markdown, manifest, "images/atp")
	if strings.Contains(got, "![") {
		t.Errorf("linkImages invented a link for a missing image:\n%s", got)
	}
	if !strings.Contains(got, "Figure 4-2. The thing.") {
		t.Errorf("linkImages dropped the caption:\n%s", got)
	}
}

func TestLinkImages_MultipleCaptionsOneImageEach(t *testing.T) {
	t.Parallel()

	// Two figures on consecutive pages.
	markdown := "<!-- page 4 -->\n\nFigure 4-1. First.\n\n" +
		"<!-- page 5 -->\n\nFigure 5-1. Second.\n"
	manifest := map[int][]ImageRef{
		4: {{File: "a-p0004-Im0.png", Name: "Im0"}},
		5: {{File: "a-p0005-Im0.png", Name: "Im0"}},
	}

	got := LinkImages(markdown, manifest, "images/x")
	if !strings.Contains(got, "![Figure 4-1](images/x/a-p0004-Im0.png)") {
		t.Errorf("missing link for Figure 4-1:\n%s", got)
	}
	if !strings.Contains(got, "![Figure 5-1](images/x/a-p0005-Im0.png)") {
		t.Errorf("missing link for Figure 5-1:\n%s", got)
	}
}

func TestLoadManifest_RoundTrip(t *testing.T) {
	t.Parallel()

	tsv := "file\tpage\tname\twidth\theight\tbpc\tcolorspace\tfilter\tbbox_x\tbbox_y\tbbox_w\tbbox_h\n" +
		"a-p0004-Im0.png\t4\tIm0\t100\t50\t8\tDeviceRGB\tFlateDecode\t0\t0\t100\t50\n" +
		"a-p0005-Im0+Im1.png\t5\tIm0+Im1\t200\t100\t8\tstitched\tstitched\t0\t0\t200\t100\n"

	got, err := ParseManifest(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("pages: got %d, want 2", len(got))
	}
	if len(got[4]) != 1 || got[4][0].File != "a-p0004-Im0.png" || got[4][0].Name != "Im0" {
		t.Errorf("page 4 entry: got %+v", got[4])
	}
	if len(got[5]) != 1 || got[5][0].Name != "Im0+Im1" {
		t.Errorf("page 5 entry: got %+v", got[5])
	}
}
