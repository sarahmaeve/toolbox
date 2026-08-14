package pdf

import (
	"fmt"
	"strings"
)

// PageRangeError reports a requested page range that falls outside the
// document. Page numbers are 1-indexed and To is inclusive.
type PageRangeError struct {
	From      int
	To        int
	PageCount int
}

func (e *PageRangeError) Error() string {
	return fmt.Sprintf("page range %d-%d out of bounds (document has %d pages)",
		e.From, e.To, e.PageCount)
}

// recoverAsError converts a panic in the calling deferred scope into an
// error assigned to *errp. Used at every entry point that handles untrusted
// PDF bytes — an index-out-of-range or makeslice-overflow in the parser
// would otherwise terminate the host process instead of returning an error.
// When no panic occurs, any prior value of *errp is preserved.
func recoverAsError(errp *error) {
	if r := recover(); r != nil {
		*errp = fmt.Errorf("pdf: recovered from panic: %v", r)
	}
}

// ExtractText opens a PDF file and returns all text content from all pages,
// concatenated with newline separators between pages.
func ExtractText(pdfPath string) (text string, err error) {
	defer recoverAsError(&err)

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
func ExtractImages(pdfPath string) (images []Image, err error) {
	defer recoverAsError(&err)

	f, pageRefs, err := openPDFPages(pdfPath)
	if err != nil {
		return nil, err
	}
	return f.extractImageRefs(pageRefs, 1)
}

// ExtractImagePages is the selective counterpart to ExtractImages. It returns
// images only from the inclusive 1-indexed page range from..to, plus the
// document's total page count. Unrequested page image streams are not decoded.
func ExtractImagePages(pdfPath string, from, to int) (images []Image, pageCount int, err error) {
	defer recoverAsError(&err)

	if from < 1 || to < from {
		return nil, 0, fmt.Errorf("invalid page range %d-%d (expected 1 <= from <= to)", from, to)
	}

	f, pageRefs, err := openPDFPages(pdfPath)
	if err != nil {
		return nil, 0, err
	}
	pageCount = len(pageRefs)
	if to > pageCount {
		return nil, pageCount, &PageRangeError{From: from, To: to, PageCount: pageCount}
	}

	images, err = f.extractImageRefs(pageRefs[from-1:to], from)
	return images, pageCount, err
}

func (f *pdfFile) extractImageRefs(pageRefs []pdfRef, firstPage int) ([]Image, error) {
	var images []Image
	for i, ref := range pageRefs {
		pageNum := firstPage + i
		pageImages, err := f.extractPageImages(ref, pageNum)
		if err != nil {
			return nil, fmt.Errorf("extracting images from page %d: %w", pageNum, err)
		}
		images = append(images, pageImages...)
	}
	return images, nil
}

// ExtractPages opens a PDF file and returns the extracted text for the
// inclusive 1-indexed page range from..to, plus the document's total page
// count. It resolves the document page tree to validate the range, but only
// decodes fonts and content streams for the requested pages.
func ExtractPages(pdfPath string, from, to int) (pages []string, pageCount int, err error) {
	defer recoverAsError(&err)

	if from < 1 || to < from {
		return nil, 0, fmt.Errorf("invalid page range %d-%d (expected 1 <= from <= to)", from, to)
	}

	f, pageRefs, err := openPDFPages(pdfPath)
	if err != nil {
		return nil, 0, err
	}
	pageCount = len(pageRefs)
	if to > pageCount {
		return nil, pageCount, &PageRangeError{From: from, To: to, PageCount: pageCount}
	}

	pages, err = f.extractPageRefs(pageRefs[from-1:to], from)
	return pages, pageCount, err
}

// ExtractMarkdownPages is the layout-aware counterpart to ExtractPages. It
// returns one Markdown string per requested page, preserving font-declared
// emphasis, elevated/lowered text, headings, paragraphs, and footnotes when
// those signals are present in the PDF content stream.
func ExtractMarkdownPages(pdfPath string, from, to int) (pages []string, pageCount int, err error) {
	defer recoverAsError(&err)

	if from < 1 || to < from {
		return nil, 0, fmt.Errorf("invalid page range %d-%d (expected 1 <= from <= to)", from, to)
	}

	f, pageRefs, err := openPDFPages(pdfPath)
	if err != nil {
		return nil, 0, err
	}
	pageCount = len(pageRefs)
	if to > pageCount {
		return nil, pageCount, &PageRangeError{From: from, To: to, PageCount: pageCount}
	}

	pages, err = f.extractMarkdownPageRefs(pageRefs[from-1:to], from)
	return pages, pageCount, err
}

// ExtractAllPages opens a PDF file and returns the extracted text for each
// page, one string per page in document order.
func ExtractAllPages(pdfPath string) (pages []string, err error) {
	defer recoverAsError(&err)

	f, pageRefs, err := openPDFPages(pdfPath)
	if err != nil {
		return nil, err
	}
	return f.extractPageRefs(pageRefs, 1)
}

// ExtractAllPagesMarkdown opens a PDF and returns layout-aware Markdown for
// every page, one string per page in document order.
func ExtractAllPagesMarkdown(pdfPath string) (pages []string, err error) {
	defer recoverAsError(&err)

	f, pageRefs, err := openPDFPages(pdfPath)
	if err != nil {
		return nil, err
	}
	return f.extractMarkdownPageRefs(pageRefs, 1)
}

func openPDFPages(pdfPath string) (*pdfFile, []pdfRef, error) {
	f, err := openPDF(pdfPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening PDF: %w", err)
	}
	pageRefs, err := f.getPages()
	if err != nil {
		return nil, nil, fmt.Errorf("getting pages: %w", err)
	}
	return f, pageRefs, nil
}

// extractPageRefs extracts a preselected sequence of page references. firstPage
// is the 1-indexed document page number represented by pageRefs[0], used in
// error messages for ranges that do not begin at page 1.
func (f *pdfFile) extractPageRefs(pageRefs []pdfRef, firstPage int) ([]string, error) {
	pages := make([]string, 0, len(pageRefs))
	for i, ref := range pageRefs {
		text, err := f.extractPageText(ref)
		if err != nil {
			return nil, fmt.Errorf("extracting page %d: %w", firstPage+i, err)
		}
		pages = append(pages, text)
	}
	return pages, nil
}

func (f *pdfFile) extractMarkdownPageRefs(pageRefs []pdfRef, firstPage int) ([]string, error) {
	pages := make([]string, 0, len(pageRefs))
	for i, ref := range pageRefs {
		pageNum := firstPage + i
		page := f.getDict(f.resolve(ref))
		if page == nil {
			return nil, fmt.Errorf("extracting page %d: page object %d is not a dict", pageNum, ref.num)
		}
		content, err := f.getPageContent(page)
		if err != nil {
			return nil, fmt.Errorf("extracting page %d: %w", pageNum, err)
		}
		width, height := pageDimensions(page, f)
		runs := extractPositionedText(content, f.buildPageFonts(page))
		pages = append(pages, renderPageMarkdown(pageNum, width, height, runs))
	}
	return pages, nil
}
