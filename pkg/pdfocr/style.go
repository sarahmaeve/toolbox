package pdfocr

import (
	"image"
	"math"
	"unicode"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

type wordRegion struct {
	Text       string
	Start, End int // rune indexes; End is exclusive
	Bounds     ocr.Bounds
}

func observationWords(observation ocr.Observation) []wordRegion {
	runes := []rune(observation.Text)
	byIndex := make(map[int]ocr.Bounds, len(observation.Symbols))
	for _, symbol := range observation.Symbols {
		if validNormalizedBounds(symbol.Bounds) {
			byIndex[symbol.RuneIndex] = symbol.Bounds
		}
	}

	var words []wordRegion
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		var bounds ocr.Bounds
		hasBounds := false
		for i := start; i < end; i++ {
			box, found := byIndex[i]
			if !found {
				continue
			}
			if !hasBounds {
				bounds, hasBounds = box, true
			} else {
				bounds = unionBounds(bounds, box)
			}
		}
		if hasBounds {
			words = append(words, wordRegion{
				Text:   string(runes[start:end]),
				Start:  start,
				End:    end,
				Bounds: bounds,
			})
		}
		start = -1
	}
	for i, character := range runes {
		if unicode.IsLetter(character) || unicode.IsMark(character) || unicode.IsDigit(character) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(runes))
	return words
}

func validNormalizedBounds(bounds ocr.Bounds) bool {
	return bounds.Width > 0 && bounds.Height > 0 &&
		!math.IsNaN(bounds.X) && !math.IsNaN(bounds.Y) &&
		!math.IsNaN(bounds.Width) && !math.IsNaN(bounds.Height)
}

func unionBounds(a, b ocr.Bounds) ocr.Bounds {
	left := math.Min(a.X, b.X)
	bottom := math.Min(a.Y, b.Y)
	right := math.Max(a.X+a.Width, b.X+b.Width)
	top := math.Max(a.Y+a.Height, b.Y+b.Height)
	return ocr.Bounds{X: left, Y: bottom, Width: right - left, Height: top - bottom}
}

// wordSlant estimates the rightward shear of near-vertical strokes. For each
// candidate shear it deskews ink pixels and measures the sharpness of their
// vertical projection. Italic serif text normally peaks at a positive shear;
// upright roman text normally peaks near zero. improvement is relative to the
// zero-shear projection.
func wordSlant(source image.Image, bounds ocr.Bounds) (shear, improvement float64, ok bool) {
	crop, ok := normalizedCrop(source, bounds)
	if !ok || crop.Dx() < 4 || crop.Dy() < 6 {
		return 0, 0, false
	}

	type inkPoint struct{ x, y int }
	var ink []inkPoint
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			r, g, b, _ := source.At(x, y).RGBA()
			luma := (299*r + 587*g + 114*b) / 1000
			if luma < 0x8000 {
				ink = append(ink, inkPoint{x: x - crop.Min.X, y: y - crop.Min.Y})
			}
		}
	}
	if len(ink) < 20 {
		return 0, 0, false
	}

	score := func(candidate float64) float64 {
		margin := int(math.Ceil(math.Abs(candidate)*float64(crop.Dy()))) + 2
		columns := make([]int, crop.Dx()+margin*2)
		for _, point := range ink {
			// Italic tops lean right. Shifting upper pixels left makes their
			// near-vertical strokes coincide in the projection.
			shifted := point.x - int(math.Round(candidate*float64(crop.Dy()-1-point.y))) + margin
			if shifted >= 0 && shifted < len(columns) {
				columns[shifted]++
			}
		}
		var sum float64
		for _, count := range columns {
			sum += float64(count * count)
		}
		return sum / float64(len(ink))
	}

	zero := score(0)
	bestScore, bestShear := zero, 0.0
	for candidate := -0.40; candidate <= 0.4001; candidate += 0.025 {
		if candidateScore := score(candidate); candidateScore > bestScore {
			bestScore, bestShear = candidateScore, candidate
		}
	}
	if zero == 0 {
		return 0, 0, false
	}
	return bestShear, (bestScore - zero) / zero, true
}

func detectItalicSpans(source image.Image, result *ocr.Result) {
	if result == nil {
		return
	}
	for observationIndex := range result.Observations {
		observation := &result.Observations[observationIndex]
		for _, word := range observationWords(*observation) {
			hasLetter := false
			for _, character := range word.Text {
				hasLetter = hasLetter || unicode.IsLetter(character)
			}
			if !hasLetter {
				continue
			}
			shear, improvement, ok := wordSlant(source, word.Bounds)
			if !ok || shear < 0.15 || improvement < 0.05 {
				continue
			}
			observation.Styles = append(observation.Styles, ocr.StyleSpan{
				Start:  word.Start,
				End:    word.End,
				Italic: true,
			})
		}
		observation.Styles = mergeItalicSpans(observation.Text, observation.Styles)
	}
}

func mergeItalicSpans(text string, spans []ocr.StyleSpan) []ocr.StyleSpan {
	if len(spans) < 2 {
		return spans
	}
	runes := []rune(text)
	merged := make([]ocr.StyleSpan, 0, len(spans))
	for _, span := range spans {
		if len(merged) == 0 {
			merged = append(merged, span)
			continue
		}
		previous := &merged[len(merged)-1]
		bridgeable := previous.Italic && span.Italic && !previous.Bold && !span.Bold &&
			previous.End <= span.Start && span.Start <= len(runes)
		if bridgeable {
			for _, character := range runes[previous.End:span.Start] {
				if !unicode.IsSpace(character) && !unicode.IsPunct(character) {
					bridgeable = false
					break
				}
			}
		}
		if bridgeable {
			previous.End = span.End
			continue
		}
		merged = append(merged, span)
	}
	return merged
}

func normalizedCrop(source image.Image, bounds ocr.Bounds) (image.Rectangle, bool) {
	if source == nil || !validNormalizedBounds(bounds) {
		return image.Rectangle{}, false
	}
	sourceBounds := source.Bounds()
	width, height := float64(sourceBounds.Dx()), float64(sourceBounds.Dy())
	x0 := sourceBounds.Min.X + int(math.Floor(bounds.X*width))
	x1 := sourceBounds.Min.X + int(math.Ceil((bounds.X+bounds.Width)*width))
	y0 := sourceBounds.Min.Y + int(math.Floor((1-bounds.Y-bounds.Height)*height))
	y1 := sourceBounds.Min.Y + int(math.Ceil((1-bounds.Y)*height))
	crop := image.Rect(
		max(sourceBounds.Min.X, x0),
		max(sourceBounds.Min.Y, y0),
		min(sourceBounds.Max.X, x1),
		min(sourceBounds.Max.Y, y1),
	)
	return crop, crop.Dx() > 0 && crop.Dy() > 0
}
