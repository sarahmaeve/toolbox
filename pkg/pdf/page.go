package pdf

import (
	"fmt"
	"log"
	"strings"
)

// maxPageTreeDepth caps page-tree recursion. Legitimate trees are shallow
// (a balanced tree over a million pages needs depth < 8); a hostile file
// can chain /Pages nodes deep enough that recursion exhausts the goroutine
// stack — a fatal runtime error recoverAsError cannot catch, unlike the
// recoverable panics the rest of the parser is hardened against.
const maxPageTreeDepth = 256

// getPages walks the PDF page tree and returns refs to all page objects in
// order.
func (f *pdfFile) getPages() ([]pdfRef, error) {
	root := f.getDict(f.trailer["Root"])
	if root == nil {
		return nil, fmt.Errorf("no /Root in trailer")
	}
	pages := f.getDict(root["Pages"])
	if pages == nil {
		return nil, fmt.Errorf("no /Pages in root")
	}
	visited := map[int]bool{}
	if ref, ok := root["Pages"].(pdfRef); ok {
		visited[ref.num] = true
	}
	return f.collectPages(pages, visited, 0), nil
}

// collectPages recursively walks the page tree, collecting leaf page refs.
// visited holds object numbers already walked: resolve's inFlight guard
// only covers in-progress resolution, so a /Kids entry referencing an
// ancestor comes back as a cached dict and would otherwise recurse forever
// (or, with duplicated kids, blow up exponentially — each node is walked
// once, which is also correct extraction: a page has exactly one /Parent).
// depth backstops linear chains too deep for the stack; nodes beyond
// maxPageTreeDepth are dropped.
func (f *pdfFile) collectPages(node pdfDict, visited map[int]bool, depth int) []pdfRef {
	if depth >= maxPageTreeDepth {
		return nil
	}
	nodeType := f.getName(node["Type"])
	if nodeType == "Page" {
		// Caller should have descended into a Pages node; nothing to return.
		return nil
	}
	kids := f.getArray(node["Kids"])
	var refs []pdfRef
	for _, kid := range kids {
		ref, ok := kid.(pdfRef)
		if !ok {
			continue
		}
		if visited[ref.num] {
			continue
		}
		visited[ref.num] = true
		kidObj := f.getDict(kid)
		if kidObj == nil {
			continue
		}
		kidType := f.getName(kidObj["Type"])
		switch kidType {
		case "Page":
			refs = append(refs, ref)
		case "Pages":
			refs = append(refs, f.collectPages(kidObj, visited, depth+1)...)
		}
	}
	return refs
}

// extractPageText extracts text from a single page.
func (f *pdfFile) extractPageText(ref pdfRef) (string, error) {
	page := f.getDict(f.resolve(ref))
	if page == nil {
		return "", fmt.Errorf("page object %d is not a dict", ref.num)
	}

	pageFonts := f.buildPageFonts(page)
	fonts := make(map[string]cmapTable, len(pageFonts))
	for name, font := range pageFonts {
		fonts[name] = font.cmap
	}

	content, err := f.getPageContent(page)
	if err != nil {
		return "", err
	}

	return extractText(content, fonts), nil
}

type pageFont struct {
	cmap     cmapTable
	baseName string
	italic   bool
	bold     bool
}

// buildPageFonts extracts character maps and the font metadata needed by the
// Markdown renderer. Styling is derived from PDF font metadata only; it is not
// guessed from the text itself.
func (f *pdfFile) buildPageFonts(page pdfDict) map[string]pageFont {
	fonts := make(map[string]pageFont)

	resources := f.getDict(f.inheritedPageValue(page, "Resources"))
	if resources == nil {
		return fonts
	}
	fontDict := f.getDict(resources["Font"])
	if fontDict == nil {
		return fonts
	}

	for name, fontRef := range fontDict {
		font := f.getDict(fontRef)
		if font == nil {
			log.Printf("warning: font %q could not be resolved", name)
			continue
		}

		baseName := f.getName(font["BaseFont"])
		lowerName := strings.ToLower(baseName)
		info := pageFont{
			baseName: baseName,
			italic: strings.Contains(lowerName, "italic") ||
				strings.Contains(lowerName, "oblique"),
			bold: strings.Contains(lowerName, "bold") ||
				strings.Contains(lowerName, "demi") ||
				strings.Contains(lowerName, "black"),
		}
		if descriptor := f.getDict(font["FontDescriptor"]); descriptor != nil {
			if angle, ok := descriptor["ItalicAngle"].(pdfNumber); ok && angle != 0 {
				info.italic = true
			}
		}

		toUnicode := font["ToUnicode"]
		if toUnicode == nil {
			info.cmap = f.buildEncodingMap(font)
			fonts[name] = info
			continue
		}
		stream, ok := f.getStream(toUnicode)
		if !ok {
			log.Printf("warning: font %q ToUnicode stream could not be read", name)
			info.cmap = f.buildEncodingMap(font)
			fonts[name] = info
			continue
		}
		decoded, err := f.decodeStream(*stream)
		if err != nil {
			log.Printf("warning: font %q ToUnicode stream decode error: %v", name, err)
			info.cmap = f.buildEncodingMap(font)
			fonts[name] = info
			continue
		}
		info.cmap = parseCMap(decoded)
		fonts[name] = info
	}

	return fonts
}

// inheritedPageValue resolves an inheritable page-tree attribute. Resources
// and page boxes are commonly stored once on a /Pages ancestor rather than on
// every leaf /Page object (PDF 1.7, table 30).
func (f *pdfFile) inheritedPageValue(page pdfDict, key string) any {
	current := page
	for depth := 0; current != nil && depth < maxPageTreeDepth; depth++ {
		if value, ok := current[key]; ok {
			return value
		}
		current = f.getDict(current["Parent"])
	}
	return nil
}

// buildEncodingMap creates a basic character map when no ToUnicode CMap is
// available. Uses WinAnsiEncoding as the default.
func (f *pdfFile) buildEncodingMap(font pdfDict) cmapTable {
	_ = font // reserved for /Encoding lookup once needed
	table := make(cmapTable)
	for i := 32; i < 128; i++ {
		table[uint16(i)] = string(rune(i))
	}
	winAnsi := map[byte]rune{
		0x80: 0x20AC,
		0x82: 0x201A,
		0x83: 0x0192,
		0x84: 0x201E,
		0x85: 0x2026,
		0x86: 0x2020,
		0x87: 0x2021,
		0x88: 0x02C6,
		0x89: 0x2030,
		0x8A: 0x0160,
		0x8B: 0x2039,
		0x8C: 0x0152,
		0x8E: 0x017D,
		0x91: 0x2018,
		0x92: 0x2019,
		0x93: 0x201C,
		0x94: 0x201D,
		0x95: 0x2022,
		0x96: 0x2013,
		0x97: 0x2014,
		0x98: 0x02DC,
		0x99: 0x2122,
		0x9A: 0x0161,
		0x9B: 0x203A,
		0x9C: 0x0153,
		0x9E: 0x017E,
		0x9F: 0x0178,
	}
	for i := 128; i < 256; i++ {
		if r, ok := winAnsi[byte(i)]; ok {
			table[uint16(i)] = string(r)
		} else {
			table[uint16(i)] = string(rune(i))
		}
	}
	return table
}

// getPageContent extracts and concatenates content stream(s) from a page.
func (f *pdfFile) getPageContent(page pdfDict) ([]byte, error) {
	contents := page["Contents"]
	if contents == nil {
		return nil, nil
	}

	switch v := f.resolve(contents).(type) {
	case pdfStream:
		return f.decodeStream(v)
	case pdfArray:
		return f.decodeStreamArray(v)
	default:
		if stream, ok := f.getStream(contents); ok {
			return f.decodeStream(*stream)
		}
		return nil, fmt.Errorf("unexpected Contents type: %T", v)
	}
}

// decodeStreamArray concatenates decoded data from an array of stream refs.
func (f *pdfFile) decodeStreamArray(arr pdfArray) ([]byte, error) {
	var result []byte
	for i, item := range arr {
		stream, ok := f.getStream(item)
		if !ok {
			continue
		}
		decoded, err := f.decodeStream(*stream)
		if err != nil {
			ref, _ := item.(pdfRef)
			return nil, fmt.Errorf("stream %d (obj %d): %w", i, ref.num, err)
		}
		result = append(result, decoded...)
		result = append(result, '\n')
	}
	return result, nil
}
