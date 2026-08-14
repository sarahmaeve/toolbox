package pdfocr

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"strings"
	"unicode"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
	xdraw "golang.org/x/image/draw"
)

func refineSuperscripts(ctx context.Context, pageImage image.Image, engine ocr.Engine, options ocr.Options, result *ocr.Result) error {
	options.CollectSymbolBounds = false
	for observationIndex := range result.Observations {
		observation := &result.Observations[observationIndex]
		runes := []rune(observation.Text)
		seenBounds := make(map[ocr.Bounds]struct{})
		for symbolIndex := range observation.Symbols {
			symbol := observation.Symbols[symbolIndex]
			if !possibleSuperscript(symbol, runes) {
				continue
			}
			if _, seen := seenBounds[symbol.Bounds]; seen {
				continue
			}
			seenBounds[symbol.Bounds] = struct{}{}

			crop, ok := scaledBoundsCrop(pageImage, symbol.Bounds, 4)
			if !ok {
				continue
			}
			refined, err := engine.Recognize(ctx, crop, options)
			if err != nil {
				return fmt.Errorf("re-read %q at rune %d: %w", symbol.Text, symbol.RuneIndex, err)
			}
			digits := isolatedDigits(refined)
			if digits == "" {
				continue
			}
			replaceObservationRange(observation, symbol.RuneIndex, symbol.RuneIndex+1, digits, true)
			runes = []rune(observation.Text)
		}
	}
	return nil
}

func possibleSuperscript(symbol ocr.Symbol, text []rune) bool {
	if symbol.RuneIndex < 0 || symbol.RuneIndex >= len(text) {
		return false
	}
	character := text[symbol.RuneIndex]
	if !strings.ContainsRune("'\"&*", character) {
		return false
	}
	if symbol.RuneIndex == 0 {
		return character == '&' || character == '*'
	}
	previous := text[symbol.RuneIndex-1]
	return unicode.IsPunct(previous)
}

func isolatedDigits(result ocr.Result) string {
	for _, observation := range result.Observations {
		candidate := strings.TrimSpace(observation.Text)
		if candidate == "" || len([]rune(candidate)) > 3 {
			continue
		}
		allDigits := true
		for _, character := range candidate {
			if !unicode.IsDigit(character) {
				allDigits = false
				break
			}
		}
		if allDigits {
			return candidate
		}
	}
	return ""
}

func replaceRune(text string, index int, replacement string) string {
	runes := []rune(text)
	if index < 0 || index >= len(runes) {
		return text
	}
	var output strings.Builder
	for i, character := range runes {
		if i == index {
			output.WriteString(replacement)
		} else {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func scaledBoundsCrop(source image.Image, bounds ocr.Bounds, scale int) (image.Image, bool) {
	if source == nil || bounds.Width <= 0 || bounds.Height <= 0 || scale < 1 {
		return nil, false
	}
	sourceBounds := source.Bounds()
	width, height := float64(sourceBounds.Dx()), float64(sourceBounds.Dy())
	horizontalPadding := bounds.Width * 0.05
	verticalPadding := bounds.Height * 0.25
	x0 := int((bounds.X - horizontalPadding) * width)
	x1 := int((bounds.X + bounds.Width + horizontalPadding) * width)
	y0 := int((1 - bounds.Y - bounds.Height - verticalPadding) * height)
	y1 := int((1 - bounds.Y + verticalPadding) * height)
	cropBounds := image.Rect(
		max(sourceBounds.Min.X, x0),
		max(sourceBounds.Min.Y, y0),
		min(sourceBounds.Max.X, x1),
		min(sourceBounds.Max.Y, y1),
	)
	if cropBounds.Dx() <= 0 || cropBounds.Dy() <= 0 {
		return nil, false
	}
	const maxScaledPixels int64 = 20 * 1000 * 1000
	for scale > 1 && int64(cropBounds.Dx())*int64(cropBounds.Dy())*int64(scale)*int64(scale) > maxScaledPixels {
		scale--
	}
	if int64(cropBounds.Dx())*int64(cropBounds.Dy())*int64(scale)*int64(scale) > maxScaledPixels {
		return nil, false
	}
	crop := image.NewRGBA(image.Rect(0, 0, cropBounds.Dx(), cropBounds.Dy()))
	draw.Draw(crop, crop.Bounds(), source, cropBounds.Min, draw.Src)
	upscaled := image.NewRGBA(image.Rect(0, 0, crop.Bounds().Dx()*scale, crop.Bounds().Dy()*scale))
	xdraw.CatmullRom.Scale(upscaled, upscaled.Bounds(), crop, crop.Bounds(), draw.Src, nil)
	return upscaled, true
}
