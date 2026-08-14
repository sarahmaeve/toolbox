package pdfocr

import (
	"strings"
	"testing"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

func TestObservationInlineRunsRendersItalicAndSuperscript(t *testing.T) {
	t.Parallel()

	observation := ocr.Observation{
		Text: "elsewhere,7 à la Maǧnūn",
		Styles: []ocr.StyleSpan{
			{Start: 10, End: 11, Superscript: true},
			{Start: 12, End: 23, Italic: true},
		},
	}
	got := renderInlineRuns(observationInlineRuns(observation))
	want := "elsewhere,<sup>7</sup> *à la Maǧnūn*"
	if got != want {
		t.Fatalf("inline Markdown: got %q, want %q", got, want)
	}
}

func TestRenderMarkdownBuildsHeadingParagraphAndFootnote(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{
		{Text: "RUNNING HEAD", Bounds: ocr.Bounds{X: .3, Y: .94, Width: .3, Height: .015}},
		{Text: "A Section", Bounds: ocr.Bounds{X: .07, Y: .82, Width: .2, Height: .025}},
		{Text: "This is a hyphen-", Bounds: ocr.Bounds{X: .10, Y: .76, Width: .7, Height: .017}},
		{Text: "ated paragraph.", Bounds: ocr.Bounds{X: .07, Y: .74, Width: .5, Height: .017}},
		{Text: "1 A compact note.", Bounds: ocr.Bounds{X: .07, Y: .16, Width: .5, Height: .011}},
		{Text: "Its continuation.", Bounds: ocr.Bounds{X: .07, Y: .145, Width: .5, Height: .011}},
	}}
	got := RenderMarkdown(5, result)
	for _, expected := range []string{
		"<!-- PDF page 5 -->",
		"## A Section",
		"This is a hyphenated paragraph.",
		"<sup>1</sup> A compact note. Its continuation.",
	} {
		if !strings.Contains(got, expected) {
			t.Errorf("Markdown missing %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "RUNNING HEAD") {
		t.Errorf("running head was retained:\n%s", got)
	}
}

func TestRenderMarkdownDoesNotPromoteLowercaseLineWraps(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{
		{Text: "Introduction", Bounds: ocr.Bounds{X: .07, Y: .84, Width: .18, Height: .025}},
		{Text: "This paragraph reaches the end of its first line", Bounds: ocr.Bounds{X: .10, Y: .76, Width: .78, Height: .017}},
		{Text: "the ordinary continuation must remain in the paragraph", Bounds: ocr.Bounds{X: .07, Y: .73, Width: .81, Height: .017}},
		{Text: "and this is its final line.", Bounds: ocr.Bounds{X: .07, Y: .71, Width: .46, Height: .017}},
	}}
	got := RenderMarkdown(9, result)
	if !strings.Contains(got, "## Introduction") {
		t.Fatalf("real heading was not retained:\n%s", got)
	}
	if strings.Contains(got, "## the ordinary continuation") {
		t.Fatalf("lowercase line wrap became a heading:\n%s", got)
	}
	if !strings.Contains(got, "first line the ordinary continuation") {
		t.Fatalf("line wrap was not joined into its paragraph:\n%s", got)
	}
}

func TestRenderMarkdownRecognizesFirstSameSizeSectionHeading(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{
		{Text: "ARTICLE RUNNING HEAD", Bounds: ocr.Bounds{X: .30, Y: .94, Width: .40, Height: .015}},
		{Text: "Differences between the manuscripts", Bounds: ocr.Bounds{X: .075, Y: .84, Width: .48, Height: .017}},
		{Text: "This indented paragraph begins below the heading.", Bounds: ocr.Bounds{X: .10, Y: .79, Width: .74, Height: .017}},
		{Text: "Its continuation establishes the ordinary line gap.", Bounds: ocr.Bounds{X: .07, Y: .77, Width: .78, Height: .017}},
		{Text: "The paragraph then ends normally.", Bounds: ocr.Bounds{X: .07, Y: .75, Width: .60, Height: .017}},
	}}
	got := RenderMarkdown(11, result)
	if !strings.Contains(got, "## Differences between the manuscripts") {
		t.Fatalf("first same-size section heading was not recognized:\n%s", got)
	}
}

func TestRenderMarkdownFindsFootnotesWithBodyLikeBounds(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{
		{Text: "Body text occupies an ordinary line.", Bounds: ocr.Bounds{X: .10, Y: .52, Width: .78, Height: .017}},
		{Text: "Its continuation ends above the notes.", Bounds: ocr.Bounds{X: .07, Y: .50, Width: .72, Height: .017}},
		{Text: "1 A first compact note.", Bounds: ocr.Bounds{X: .07, Y: .31, Width: .58, Height: .015}},
		{Text: "Its compact continuation.", Bounds: ocr.Bounds{X: .08, Y: .294, Width: .50, Height: .015}},
		{Text: "2 A second compact note.", Bounds: ocr.Bounds{X: .07, Y: .278, Width: .60, Height: .015}},
	}}
	got := RenderMarkdown(9, result)
	if !strings.Contains(got, "---\n\n<sup>1</sup> A first compact note. Its compact continuation.") {
		t.Fatalf("footnote zone was not separated:\n%s", got)
	}
	if !strings.Contains(got, "<sup>2</sup> A second compact note.") {
		t.Fatalf("second footnote was not rendered:\n%s", got)
	}
}

func TestRenderMarkdownDoesNotTreatLowBodyNumberAsFootnote(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{
		{Text: "A body paragraph names a manuscript on this line", Bounds: ocr.Bounds{X: .10, Y: .46, Width: .78, Height: .0175}},
		{Text: "368 and another manuscript while continuing its argument", Bounds: ocr.Bounds{X: .07, Y: .4425, Width: .80, Height: .0173}},
		{Text: "across several compactly bounded lines of ordinary prose", Bounds: ocr.Bounds{X: .07, Y: .425, Width: .79, Height: .0152}},
		{Text: "before the paragraph reaches its actual conclusion.", Bounds: ocr.Bounds{X: .07, Y: .4075, Width: .72, Height: .0173}},
		{Text: "13 The genuinely separated note begins here.", Bounds: ocr.Bounds{X: .07, Y: .37, Width: .62, Height: .0173}},
		{Text: "Its compact continuation.", Bounds: ocr.Bounds{X: .08, Y: .354, Width: .48, Height: .0150}},
	}}
	got := RenderMarkdown(13, result)
	if !strings.Contains(got, "368 and another manuscript") {
		t.Fatalf("low body line disappeared into footnotes:\n%s", got)
	}
	separator := strings.Index(got, "---")
	bodyNumber := strings.Index(got, "368 and another")
	if separator < 0 || bodyNumber < 0 || bodyNumber > separator {
		t.Fatalf("low body number was placed after the footnote separator:\n%s", got)
	}
	if !strings.Contains(got, "<sup>13</sup> The genuinely separated note") {
		t.Fatalf("actual separated note was not recognized:\n%s", got)
	}
}

func TestRenderMarkdownPagesRepairsSequentialFootnoteMarkers(t *testing.T) {
	t.Parallel()

	pages := []Page{{Number: 209, OCR: ocr.Result{Observations: []ocr.Observation{
		{Text: "An Article'", Bounds: ocr.Bounds{X: .39, Y: .84, Width: .22, Height: .025}},
		{Text: "Body text with another reference.\" follows here.", Bounds: ocr.Bounds{X: .10, Y: .74, Width: .78, Height: .017}},
		{Text: "\" Acknowledgments.", Bounds: ocr.Bounds{X: .07, Y: .31, Width: .45, Height: .015}},
		{Text: "2 A bibliographic note.", Bounds: ocr.Bounds{X: .07, Y: .294, Width: .55, Height: .015}},
	}}}, {Number: 210, OCR: ocr.Result{Observations: []ocr.Observation{
		{Text: "Another body reference.'", Bounds: ocr.Bounds{X: .10, Y: .74, Width: .72, Height: .017}},
		{Text: "3 A correct note.", Bounds: ocr.Bounds{X: .07, Y: .31, Width: .45, Height: .015}},
		{Text: "9 A digit misread by OCR.", Bounds: ocr.Bounds{X: .07, Y: .294, Width: .60, Height: .015}},
	}}}, {Number: 211, OCR: ocr.Result{Observations: []ocr.Observation{
		{Text: "A short body line precedes the final note.", Bounds: ocr.Bounds{X: .10, Y: .50, Width: .68, Height: .017}},
		{Text: "5 A later numeric anchor.", Bounds: ocr.Bounds{X: .07, Y: .31, Width: .55, Height: .015}},
	}}}}

	got := RenderMarkdownPages(pages)
	joined := strings.Join(got, "\n")
	for _, expected := range []string{
		"An Article<sup>1</sup>",
		"reference.<sup>2</sup> follows",
		"<sup>1</sup> Acknowledgments.",
		"<sup>2</sup> A bibliographic note.",
		"reference.<sup>3</sup>",
		"<sup>3</sup> A correct note.",
		"<sup>4</sup> A digit misread by OCR.",
		"<sup>5</sup> A later numeric anchor.",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("document Markdown missing %q:\n%s", expected, joined)
		}
	}
}

func TestBodyCitationCandidatesRejectsOCROrdinalCluster(t *testing.T) {
	t.Parallel()

	lines, _, _, _ := prepareMarkdownLines(ocr.Result{Observations: []ocr.Observation{
		{Text: "It was copied in the 16*' century, in Rome;\" then circulated.", Bounds: ocr.Bounds{X: .07, Y: .74, Width: .82, Height: .017}},
		{Text: "An ordinary continuation supplies line geometry.", Bounds: ocr.Bounds{X: .07, Y: .72, Width: .72, Height: .017}},
	}})
	candidates := bodyCitationCandidates(lines)
	if len(candidates) != 1 {
		t.Fatalf("citation candidates: got %+v, want only the marker after the semicolon", candidates)
	}
	observation := lines[0].observation
	got := string([]rune(observation.Text)[candidates[0].start:candidates[0].end])
	if got != "\"" {
		t.Fatalf("citation candidate: got %q, want quote after semicolon", got)
	}
}

func TestBodyCitationCandidatesFindsAttachedNumericAndConfusedMarkers(t *testing.T) {
	t.Parallel()

	lines, _, _, _ := prepareMarkdownLines(ocr.Result{Observations: []ocr.Observation{
		{Text: "An author died in 1286.10 and another claim ended.1l Here.", Bounds: ocr.Bounds{X: .07, Y: .74, Width: .82, Height: .017}},
		{Text: "A normal 3.14 value is followed by prose.", Bounds: ocr.Bounds{X: .07, Y: .72, Width: .70, Height: .017}},
	}})
	candidates := bodyCitationCandidates(lines)
	if len(candidates) != 3 {
		t.Fatalf("citation candidates: got %+v, want 1286.10, .1l, and conservative 3.14 candidates", candidates)
	}
	if candidates[0].value != 10 || candidates[1].value != 0 || candidates[2].value != 14 {
		t.Fatalf("citation candidate values: got %+v", candidates)
	}
}

func TestBodyCitationCandidatesConsumesPunctuationDigitCluster(t *testing.T) {
	t.Parallel()

	lines, _, _, _ := prepareMarkdownLines(ocr.Result{Observations: []ocr.Observation{
		{Text: "A claim ended.'5 The next sentence begins.", Bounds: ocr.Bounds{X: .07, Y: .74, Width: .74, Height: .017}},
		{Text: "An ordinary continuation supplies line geometry.", Bounds: ocr.Bounds{X: .07, Y: .72, Width: .72, Height: .017}},
	}})
	candidates := bodyCitationCandidates(lines)
	if len(candidates) != 1 {
		t.Fatalf("citation candidates: got %+v, want one compact cluster", candidates)
	}
	got := string([]rune(lines[0].observation.Text)[candidates[0].start:candidates[0].end])
	if got != "'5" {
		t.Fatalf("citation cluster: got %q, want %q", got, "'5")
	}
}

func TestFootnoteMarkerPrefixAcceptsFragmentedAndConfusedMarkers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		text  string
		value int
	}{
		{"26", 26},
		{"3A Ibid., pp. 28-29.", 0},
		{"s0 To be published", 0},
		{"5' For this interpretation", 0},
	}
	for _, test := range tests {
		_, _, value, ok := footnoteMarkerPrefix(test.text)
		if !ok || value != test.value {
			t.Errorf("footnoteMarkerPrefix(%q): got value=%d ok=%v, want value=%d ok=true", test.text, value, ok, test.value)
		}
	}
	for _, text := range []string{"Ibid., pp. 12-13.", "Other short narratives", "368"} {
		if _, _, _, ok := footnoteMarkerPrefix(text); ok {
			t.Errorf("footnoteMarkerPrefix(%q) unexpectedly matched", text)
		}
	}
}

func TestJoinOCRLineCarriesStyleAcrossHyphenatedWord(t *testing.T) {
	t.Parallel()

	paragraph := []inlineRun{{text: "A *literal* ", flags: 0}, {text: "Andalu-", flags: flagItalic}}
	line := []inlineRun{{text: "sian ", flags: 0}, {text: "Courtly Culture", flags: flagItalic}}
	got := renderInlineRuns(joinOCRLine(paragraph, line, "sian Courtly Culture"))
	want := "A \\*literal\\* *Andalusian* *Courtly Culture*"
	if got != want {
		t.Fatalf("joined styled word: got %q, want %q", got, want)
	}
}
