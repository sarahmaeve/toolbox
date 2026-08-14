// Package ocr defines the engine-neutral boundary between page-image
// extraction and platform- or service-specific optical character recognition.
package ocr

import (
	"context"
	"errors"
	"image"
	"strings"
)

// ErrUnavailable means an OCR engine cannot run on the current platform.
var ErrUnavailable = errors.New("OCR engine unavailable on this platform")

// Bounds is a normalized rectangle with a lower-left origin. Keeping the
// engine's geometry makes later layout-aware Markdown possible without tying
// the PDF package to a particular OCR implementation.
type Bounds struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// Observation is one recognized line or text region.
type Observation struct {
	Text       string
	Confidence float32
	Bounds     Bounds
	Candidates []Candidate
	Symbols    []Symbol
	Styles     []StyleSpan
}

// Candidate is one of the engine's ranked readings for an observation. The
// first candidate normally matches Observation.Text. Retaining alternatives
// lets higher-level OCR policy improve uncertain scholarly text without
// rerunning the native recognizer.
type Candidate struct {
	Text       string
	Confidence float32
}

// Symbol records the recognized text and geometry of one Unicode code point
// in the primary candidate. Backends populate it only when
// Options.CollectSymbolBounds is true.
type Symbol struct {
	Text        string
	RuneIndex   int
	Bounds      Bounds
	Superscript bool
}

// StyleSpan applies OCR-derived inline formatting to a half-open rune range
// in Observation.Text.
type StyleSpan struct {
	Start, End  int
	Italic      bool
	Bold        bool
	Superscript bool
	Subscript   bool
}

// Result is the OCR output for one raster page.
type Result struct {
	Width        int
	Height       int
	Observations []Observation
}

// Text returns the recognized regions in engine-provided reading order.
func (r Result) Text() string {
	lines := make([]string, 0, len(r.Observations))
	for _, observation := range r.Observations {
		if line := strings.TrimSpace(observation.Text); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// Options are portable recognition controls. An empty Languages slice asks
// the backend to detect the language when it supports doing so. CustomWords
// provide an OCR-time vocabulary hint; engines may require language-model
// correction to be enabled before they take effect.
type Options struct {
	Languages           []string
	LanguageCorrection  bool
	CustomWords         []string
	CollectSymbolBounds bool
}

// Engine recognizes text in a decoded page image.
type Engine interface {
	Name() string
	Recognize(context.Context, image.Image, Options) (Result, error)
}
