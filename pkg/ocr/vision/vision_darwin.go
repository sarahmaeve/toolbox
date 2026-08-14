//go:build darwin

// Package vision implements OCR with Apple's on-device Vision framework.
package vision

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"unicode/utf16"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
	"github.com/tmc/apple/foundation"
	applevision "github.com/tmc/apple/vision"
)

// Engine performs accurate, local OCR through Apple's Vision framework.
type Engine struct{}

func New() *Engine { return &Engine{} }

func (*Engine) Name() string { return "vision" }

// SupportedLanguages reports the language identifiers available to accurate
// text recognition on this macOS installation.
func (*Engine) SupportedLanguages() ([]string, error) {
	request := applevision.NewVNRecognizeTextRequest()
	request.SetRecognitionLevel(applevision.VNRequestTextRecognitionLevelAccurate)
	languages, err := request.SupportedRecognitionLanguagesAndReturnError()
	if err != nil {
		return nil, fmt.Errorf("vision OCR: list supported languages: %w", err)
	}
	return languages, nil
}

func (*Engine) Recognize(ctx context.Context, img image.Image, options ocr.Options) (ocr.Result, error) {
	if img == nil {
		return ocr.Result{}, fmt.Errorf("vision OCR: nil image")
	}
	if err := ctx.Err(); err != nil {
		return ocr.Result{}, err
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return ocr.Result{}, fmt.Errorf("vision OCR: encode page image: %w", err)
	}
	data := foundation.NewDataFromBytes(encoded.Bytes())
	if data.ID == 0 {
		return ocr.Result{}, fmt.Errorf("vision OCR: create image data")
	}

	handler := applevision.NewImageRequestHandlerWithDataOptions(data, nil)
	request := applevision.NewVNRecognizeTextRequest()
	request.SetRecognitionLevel(applevision.VNRequestTextRecognitionLevelAccurate)
	request.SetUsesLanguageCorrection(options.LanguageCorrection)
	if len(options.CustomWords) > 0 {
		request.SetCustomWords(options.CustomWords)
	}
	if len(options.Languages) > 0 {
		request.SetRecognitionLanguages(options.Languages)
	} else {
		request.SetAutomaticallyDetectsLanguage(true)
	}

	ok, err := handler.PerformRequestsError([]applevision.VNRequest{
		applevision.VNRequestFromID(request.ID),
	})
	if err != nil {
		return ocr.Result{}, fmt.Errorf("vision OCR: recognize text: %w", err)
	}
	if !ok {
		return ocr.Result{}, fmt.Errorf("vision OCR: recognize text returned false")
	}
	if err := ctx.Err(); err != nil {
		return ocr.Result{}, err
	}

	bounds := img.Bounds()
	result := ocr.Result{Width: bounds.Dx(), Height: bounds.Dy()}
	for _, observation := range request.Results() {
		textObservation := applevision.VNRecognizedTextObservationFromID(observation.ID)
		candidates := textObservation.TopCandidates(10)
		if len(candidates) == 0 {
			continue
		}
		box := textObservation.BoundingBox()
		recognized := ocr.Observation{
			Text:       candidates[0].String(),
			Confidence: float32(candidates[0].Confidence()),
			Bounds: ocr.Bounds{
				X:      box.Origin.X,
				Y:      box.Origin.Y,
				Width:  box.Size.Width,
				Height: box.Size.Height,
			},
		}
		for _, candidate := range candidates {
			recognized.Candidates = append(recognized.Candidates, ocr.Candidate{
				Text:       candidate.String(),
				Confidence: float32(candidate.Confidence()),
			})
		}
		if options.CollectSymbolBounds {
			recognized.Symbols = recognizedSymbols(candidates[0])
		}
		result.Observations = append(result.Observations, recognized)
	}
	return result, nil
}

func recognizedSymbols(candidate applevision.VNRecognizedText) []ocr.Symbol {
	text := candidate.String()
	symbols := make([]ocr.Symbol, 0, len([]rune(text)))
	utf16Offset := 0
	for runeIndex, character := range []rune(text) {
		length := utf16.RuneLen(character)
		if length < 1 {
			length = 1
		}
		rectangle, err := candidate.BoundingBoxForRangeError(foundation.NSRange{
			Location: uint(utf16Offset),
			Length:   uint(length),
		})
		utf16Offset += length
		if err != nil {
			continue
		}
		box := rectangle.BoundingBox()
		symbols = append(symbols, ocr.Symbol{
			Text:      string(character),
			RuneIndex: runeIndex,
			Bounds: ocr.Bounds{
				X:      box.Origin.X,
				Y:      box.Origin.Y,
				Width:  box.Size.Width,
				Height: box.Size.Height,
			},
		})
	}
	return symbols
}

var _ ocr.Engine = (*Engine)(nil)
