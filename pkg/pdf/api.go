package pdf

import (
	"fmt"
	"strings"
)

// ExtractText opens a PDF file and returns all text content from all pages,
// concatenated with newline separators between pages.
func ExtractText(pdfPath string) (string, error) {
	pages, err := ExtractAllPages(pdfPath)
	if err != nil {
		return "", err
	}
	return strings.Join(pages, "\n"), nil
}

// ExtractImages opens a PDF file and returns every Image XObject it finds,
// page by page in document order. Each Image carries ready-to-write bytes in
// the encoding implied by its Ext field.
//
// Unsupported filter combinations (e.g. JBIG2Decode) are logged and skipped
// rather than returned as errors, so a single odd image cannot poison an
// otherwise-extractable document.
func ExtractImages(pdfPath string) ([]Image, error) {
	f, err := openPDF(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("opening PDF: %w", err)
	}

	pageRefs, err := f.getPages()
	if err != nil {
		return nil, fmt.Errorf("getting pages: %w", err)
	}

	var images []Image
	for i, ref := range pageRefs {
		pageImages, err := f.extractPageImages(ref, i+1)
		if err != nil {
			return nil, fmt.Errorf("extracting images from page %d: %w", i+1, err)
		}
		images = append(images, pageImages...)
	}

	return images, nil
}

// ExtractAllPages opens a PDF file and returns the extracted text for each
// page, one string per page in document order.
func ExtractAllPages(pdfPath string) ([]string, error) {
	f, err := openPDF(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("opening PDF: %w", err)
	}

	pageRefs, err := f.getPages()
	if err != nil {
		return nil, fmt.Errorf("getting pages: %w", err)
	}

	pages := make([]string, 0, len(pageRefs))
	for i, ref := range pageRefs {
		text, err := f.extractPageText(ref)
		if err != nil {
			return nil, fmt.Errorf("extracting page %d: %w", i+1, err)
		}
		pages = append(pages, text)
	}

	return pages, nil
}
