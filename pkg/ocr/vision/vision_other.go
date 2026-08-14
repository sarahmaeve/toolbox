//go:build !darwin

// Package vision provides the cross-platform stub for Apple's Vision OCR.
package vision

import (
	"context"
	"fmt"
	"image"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

type Engine struct{}

func New() *Engine { return &Engine{} }

func (*Engine) Name() string { return "vision" }

func (*Engine) SupportedLanguages() ([]string, error) {
	return nil, fmt.Errorf("vision OCR: %w", ocr.ErrUnavailable)
}

func (*Engine) Recognize(context.Context, image.Image, ocr.Options) (ocr.Result, error) {
	return ocr.Result{}, fmt.Errorf("vision OCR: %w", ocr.ErrUnavailable)
}

var _ ocr.Engine = (*Engine)(nil)
