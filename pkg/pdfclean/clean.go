// Package pdfclean transforms raw text extracted from a PDF (e.g. by
// pkg/pdf's ExtractAllPages or the toolbox-pdf dump command) into a
// readable markdown working copy.
//
// The cleaner is deliberately conservative — it does NOT reflow
// paragraphs, promote headings, or guess at document structure. It
// targets specific PDF-extraction artifacts:
//
//   - hyphen-split fragments ("15\n-\nта" → "15-та")
//   - bracketed citations ("[\n3\n]" → "[3]")
//   - numeric ranges split around an en-dash
//   - em-dash splits in figure captions and definitions
//   - excess blank lines
//   - form feeds, which become <!-- page N --> markers so the reader
//     can map back to the source PDF
//
// When a pkg/pdf-style image manifest is available, LinkImages inserts
// Markdown image links above caption lines it recognizes. Caption
// matching is language-aware for English, Ukrainian, and Russian.
package pdfclean

import (
	"fmt"
	"regexp"
	"strings"
)

const wordChar = `[\p{L}\p{N}]`

var (
	// hyphenSplitRe matches a word fragment, an isolated ASCII hyphen on its own
	// line, and the next word fragment. Targets pdfdump output like "15\n-\nта"
	// (Ukrainian ordinal) or "Москва\n-\n1" (product model code).
	hyphenSplitRe = regexp.MustCompile(`(` + wordChar + `+)\s*\n\s*-\s*\n\s*(` + wordChar + `+)`)

	// citationRe matches "[\n3\n]" with any whitespace, used for bracketed
	// numeric citation markers split across lines.
	citationRe = regexp.MustCompile(`\[\s*\n+\s*(\d+)\s*\n+\s*\]`)

	// numRangeRe matches a numeric range split around an en-dash, e.g.
	// "1,5\n–\n1,8" — these are quantities and should join without spaces.
	numRangeRe = regexp.MustCompile(`(\d[\d,.]*)[ \t]*\n+[ \t]*–[ \t]*\n+[ \t]*(\d)`)

	// prosEmDashRe matches a non-numeric token, an isolated en-dash on its own
	// line, and the next non-numeric token. Rejoins figure captions and
	// definitions like "Рисунок 3\n\n–\n\nКомплекс". Join with surrounding spaces.
	prosEmDashRe = regexp.MustCompile(`(\S)[ \t]*\n+[ \t]*–[ \t]*\n+[ \t]*(\S)`)

	// formFeedRe matches a form-feed page separator with surrounding newlines,
	// as emitted by pdfdump between pages.
	formFeedRe = regexp.MustCompile(`\n*\f\n*`)

	// blankRunRe matches three or more consecutive line breaks (= two or more
	// blank lines).
	blankRunRe = regexp.MustCompile(`\n{3,}`)
)

// Clean transforms raw extracted PDF text into a markdown working
// copy. It applies conservative repairs targeting known extraction
// artifacts (isolated hyphens, em-dashes, bracketed citations, excess
// blank lines) and inserts explicit page markers so a reader can map
// back to the source PDF. It does NOT reflow paragraphs or detect
// headings — the goal is a faithful reading copy, not a typeset doc.
//
// Input is expected to use form-feed characters between pages, which
// is what pkg/pdf's text-extraction layer (and the toolbox-pdf dump
// command) emits.
func Clean(s string) string {
	// Rejoin hyphen-split fragments. Iterate to handle chains like
	// "Ростов\n-\nна\n-\nДону" which need multiple passes.
	for {
		next := hyphenSplitRe.ReplaceAllString(s, "$1-$2")
		if next == s {
			break
		}
		s = next
	}

	s = citationRe.ReplaceAllString(s, "[$1]")

	// Numeric ranges first (no spaces around en-dash), then prose em-dashes
	// (with spaces). Order matters: prosEmDashRe would otherwise consume
	// numeric range matches and add unwanted spaces.
	s = numRangeRe.ReplaceAllString(s, "$1–$2")
	s = prosEmDashRe.ReplaceAllString(s, "$1 – $2")

	pageNum := 1
	s = formFeedRe.ReplaceAllStringFunc(s, func(string) string {
		pageNum++
		return fmt.Sprintf("\n\n<!-- page %d -->\n\n", pageNum)
	})

	s = blankRunRe.ReplaceAllString(s, "\n\n")

	s = strings.TrimSpace(s)
	return "<!-- page 1 -->\n\n" + s + "\n"
}
