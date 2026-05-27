package pdf

import (
	"fmt"
	"log"
)

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
	return f.collectPages(pages), nil
}

// collectPages recursively walks the page tree, collecting leaf page refs.
func (f *pdfFile) collectPages(node pdfDict) []pdfRef {
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
		kidObj := f.getDict(kid)
		if kidObj == nil {
			continue
		}
		kidType := f.getName(kidObj["Type"])
		switch kidType {
		case "Page":
			refs = append(refs, ref)
		case "Pages":
			refs = append(refs, f.collectPages(kidObj)...)
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

	fonts := f.buildFontMaps(page)

	content, err := f.getPageContent(page)
	if err != nil {
		return "", err
	}

	return extractText(content, fonts), nil
}

// buildFontMaps extracts ToUnicode CMaps for all fonts on a page.
func (f *pdfFile) buildFontMaps(page pdfDict) map[string]cmapTable {
	fonts := make(map[string]cmapTable)

	resources := f.getDict(page["Resources"])
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
		toUnicode := font["ToUnicode"]
		if toUnicode == nil {
			fonts[name] = f.buildEncodingMap(font)
			continue
		}
		stream, ok := f.getStream(toUnicode)
		if !ok {
			log.Printf("warning: font %q ToUnicode stream could not be read", name)
			fonts[name] = f.buildEncodingMap(font)
			continue
		}
		decoded, err := f.decodeStream(*stream)
		if err != nil {
			log.Printf("warning: font %q ToUnicode stream decode error: %v", name, err)
			fonts[name] = f.buildEncodingMap(font)
			continue
		}
		fonts[name] = parseCMap(decoded)
	}

	return fonts
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
