package pdfocr

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
	xdraw "golang.org/x/image/draw"
)

// refineFootnoteRegion re-runs the configured OCR engine on the bottom text
// region at a larger effective scale. Full-page recognition can omit small
// note lines or merge adjacent markers even when the page raster itself is
// sharp. Cropping is derived from the detected body/footnote boundary; no page
// number, note number, or document-specific text is involved.
func refineFootnoteRegion(ctx context.Context, pageImage image.Image, engine ocr.Engine, options ocr.Options, result *ocr.Result) error {
	if pageImage == nil || engine == nil || result == nil {
		return nil
	}
	lines := markdownLines(*result)
	if len(lines) == 0 {
		return nil
	}
	bodyHeight := dominantOCRLineHeight(lines)
	markRunningHeaders(lines)
	normalGap := dominantOCRLineGap(lines, bodyHeight)
	start := findFootnoteStart(lines, bodyHeight, normalGap)
	if start < 0 {
		return nil
	}

	previous := start - 1
	for previous >= 0 && lines[previous].running {
		previous--
	}
	if previous < 0 {
		return nil
	}
	// The midpoint between the last body baseline and the first detected note's
	// top safely includes a note omitted by the full-page pass while excluding
	// the body line above the rule.
	boundary := (lines[previous].y + lines[start].top) / 2
	boundary = max(lines[start].top, min(lines[previous].y, boundary))
	cropTop := min(0.55, boundary+normalGap*0.75)
	crop, verticalSpan, ok := enlargedBottomRegion(pageImage, cropTop, 2)
	if !ok {
		return nil
	}

	refined, err := engine.Recognize(ctx, crop, options)
	if err != nil {
		return fmt.Errorf("recognize enlarged lower-page region: %w", err)
	}
	remapBottomRegion(&refined, verticalSpan, result.Width, result.Height)
	mergeRefinedBottomRegion(result, refined, boundary)
	return nil
}

func enlargedBottomRegion(source image.Image, normalizedTop float64, scale int) (image.Image, float64, bool) {
	if source == nil || normalizedTop <= 0 || normalizedTop > 1 || scale < 1 {
		return nil, 0, false
	}
	bounds := source.Bounds()
	y0 := bounds.Min.Y + int(math.Floor((1-normalizedTop)*float64(bounds.Dy())))
	y0 = max(bounds.Min.Y, min(bounds.Max.Y-1, y0))
	region := image.Rect(bounds.Min.X, y0, bounds.Max.X, bounds.Max.Y)
	if region.Dx() <= 0 || region.Dy() <= 0 {
		return nil, 0, false
	}

	const maxScaledPixels int64 = 20 * 1000 * 1000
	for scale > 1 && int64(region.Dx())*int64(region.Dy())*int64(scale)*int64(scale) > maxScaledPixels {
		scale--
	}
	crop := image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
	draw.Draw(crop, crop.Bounds(), source, region.Min, draw.Src)
	if scale == 1 {
		return crop, float64(region.Dy()) / float64(bounds.Dy()), true
	}
	upscaled := image.NewRGBA(image.Rect(0, 0, crop.Bounds().Dx()*scale, crop.Bounds().Dy()*scale))
	xdraw.CatmullRom.Scale(upscaled, upscaled.Bounds(), crop, crop.Bounds(), draw.Src, nil)
	return upscaled, float64(region.Dy()) / float64(bounds.Dy()), true
}

func remapBottomRegion(result *ocr.Result, verticalSpan float64, pageWidth, pageHeight int) {
	if result == nil {
		return
	}
	for observationIndex := range result.Observations {
		observation := &result.Observations[observationIndex]
		observation.Bounds.Y *= verticalSpan
		observation.Bounds.Height *= verticalSpan
		for symbolIndex := range observation.Symbols {
			observation.Symbols[symbolIndex].Bounds.Y *= verticalSpan
			observation.Symbols[symbolIndex].Bounds.Height *= verticalSpan
		}
	}
	result.Width = pageWidth
	result.Height = pageHeight
}

func mergeRefinedBottomRegion(result *ocr.Result, refined ocr.Result, boundary float64) bool {
	if result == nil || boundary <= 0 || boundary >= 1 {
		return false
	}
	inside := func(observation ocr.Observation) bool {
		return observation.Bounds.Y+observation.Bounds.Height/2 <= boundary
	}

	var originalBottom, refinedBottom []ocr.Observation
	for _, observation := range result.Observations {
		if inside(observation) {
			originalBottom = append(originalBottom, observation)
		}
	}
	for _, observation := range refined.Observations {
		if strings.TrimSpace(observation.Text) != "" && validNormalizedBounds(observation.Bounds) && inside(observation) {
			refinedBottom = append(refinedBottom, observation)
		}
	}
	if !preferRefinedSmallText(originalBottom, refinedBottom) {
		return false
	}

	merged := make([]ocr.Observation, 0, len(result.Observations)-len(originalBottom)+len(refinedBottom))
	for _, observation := range result.Observations {
		if !inside(observation) {
			merged = append(merged, observation)
		}
	}
	merged = append(merged, refinedBottom...)
	sort.SliceStable(merged, func(i, j int) bool {
		iTop := merged[i].Bounds.Y + merged[i].Bounds.Height
		jTop := merged[j].Bounds.Y + merged[j].Bounds.Height
		if math.Abs(iTop-jTop) > 0.004 {
			return iTop > jTop
		}
		return merged[i].Bounds.X < merged[j].Bounds.X
	})
	result.Observations = merged
	return true
}

func preferRefinedSmallText(original, refined []ocr.Observation) bool {
	if len(refined) == 0 {
		return false
	}
	quality := func(observations []ocr.Observation) (markers, characters int) {
		for _, observation := range observations {
			if _, _, _, ok := footnoteMarkerPrefix(observation.Text); ok {
				markers++
			}
			characters += utf8.RuneCountInString(strings.TrimSpace(observation.Text))
		}
		return markers, characters
	}
	originalMarkers, originalCharacters := quality(original)
	refinedMarkers, refinedCharacters := quality(refined)
	return refinedMarkers >= originalMarkers && refinedCharacters*4 >= originalCharacters*3
}
