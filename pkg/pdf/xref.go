package pdf

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"strconv"
)

const maxPDFSize = 100 * 1024 * 1024 // 100 MB

// validXrefOffset extracts a non-negative integer offset from a pdfNumber
// (a float64 under the hood). Per PDF 1.7 §7.5.5/§7.5.8 cross-reference
// offsets must be non-negative integers; a hostile file can encode any
// float, and Go's float→int64 conversion is implementation-defined for
// out-of-range values. Reject NaN/Inf, negatives, fractionals, and
// anything that doesn't fit cleanly in int64.
func validXrefOffset(n pdfNumber) (int64, bool) {
	v := float64(n)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if v < 0 || v > math.MaxInt64 {
		return 0, false
	}
	if v != math.Trunc(v) {
		return 0, false
	}
	return int64(v), true
}

// openPDF reads the entire PDF file into memory, verifies the header, parses
// the cross-reference table(s) and trailer, and returns a ready-to-use file.
func openPDF(path string) (*pdfFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat pdf file: %w", err)
	}
	if info.Size() > maxPDFSize {
		return nil, fmt.Errorf("pdf file too large: %d bytes (max %d)", info.Size(), maxPDFSize)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is user-supplied by design for a CLI extractor
	if err != nil {
		return nil, fmt.Errorf("reading pdf file: %w", err)
	}

	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, fmt.Errorf("not a pdf file: missing %%PDF- header")
	}

	f := &pdfFile{
		data:    data,
		xref:    map[int]xrefEntry{},
		cache:   map[int]any{},
		objStms: map[int]*objStm{},
	}

	if err := f.parseXref(); err != nil {
		return nil, fmt.Errorf("parsing xref: %w", err)
	}

	return f, nil
}

// parseXref finds startxref and follows the xref chain (via /Prev) to build the
// complete cross-reference table. Branches to a classic xref-table parser or to
// the xref-stream parser depending on what is at the startxref offset.
func (f *pdfFile) parseXref() error {
	// Search the last 1024 bytes for "startxref".
	tail := f.data
	if len(tail) > 1024 {
		tail = tail[len(tail)-1024:]
	}

	idx := bytes.LastIndex(tail, []byte("startxref"))
	if idx < 0 {
		return fmt.Errorf("startxref not found")
	}

	pos := len(f.data) - len(tail) + idx + len("startxref")
	pos = skipWhitespace(f.data, pos)

	offset, _, err := readInt(f.data, pos)
	if err != nil {
		return fmt.Errorf("reading startxref offset: %w", err)
	}

	offsets := []int64{int64(offset)}
	visited := map[int64]bool{int64(offset): true}

	var trailers []pdfDict

	for len(offsets) > 0 {
		off := offsets[0]
		offsets = offsets[1:]

		trailer, prev, err := f.parseXrefAt(int(off))
		if err != nil {
			return fmt.Errorf("parsing xref section at %d: %w", off, err)
		}
		trailers = append(trailers, trailer)

		// Some PDFs reference a hybrid xref stream via /XRefStm in the trailer.
		if xrefStm, ok := trailer["XRefStm"]; ok {
			if n, ok := xrefStm.(pdfNumber); ok {
				stmOff, ok := validXrefOffset(n)
				if !ok {
					return fmt.Errorf("invalid /XRefStm offset %v", float64(n))
				}
				if !visited[stmOff] {
					visited[stmOff] = true
					offsets = append(offsets, stmOff)
				}
			}
		}

		if prev != 0 && !visited[prev] {
			visited[prev] = true
			offsets = append(offsets, prev)
		}
	}

	// Pick the trailer that contains /Root (the primary trailer).
	for _, t := range trailers {
		if _, hasRoot := t["Root"]; hasRoot {
			f.trailer = t
			break
		}
	}
	if f.trailer == nil && len(trailers) > 0 {
		f.trailer = trailers[0]
	}

	return nil
}

// parseXrefAt dispatches to either the classic-table or xref-stream parser
// depending on what is at offset. Returns the section's trailer dict and its
// /Prev offset (0 if absent).
func (f *pdfFile) parseXrefAt(offset int) (pdfDict, int64, error) {
	pos := offset
	pos = skipWhitespace(f.data, pos)

	// Linearized PDFs may have an indirect object before the xref keyword.
	// Skip it if present, so the classic parser can still see "xref" beyond it.
	if pos < len(f.data) && isDigit(f.data[pos]) && !f.looksLikeXrefStreamObject(pos) {
		_, newPos, err := parseValue(f.data, pos, f)
		if err != nil {
			return nil, 0, fmt.Errorf("skipping linearization object: %w", err)
		}
		pos = newPos
		pos = skipWhitespace(f.data, pos)
		if f.safeHasPrefix(pos, "endobj") {
			pos += len("endobj")
			pos = skipWhitespace(f.data, pos)
		}
	}

	if f.safeHasPrefix(pos, "xref") {
		return f.parseClassicXref(pos)
	}

	// Not "xref" — treat as an indirect object whose stream is a /Type /XRef.
	return f.parseXrefStreamAt(offset)
}

// looksLikeXrefStreamObject returns true if the bytes at pos parse as the
// header of an indirect object (N G obj) whose body is a dictionary that has
// /Type /XRef. Used to distinguish a linearization object (which should be
// skipped) from an actual cross-reference stream.
func (f *pdfFile) looksLikeXrefStreamObject(pos int) bool {
	saved := pos

	if _, newPos, err := readInt(f.data, pos); err == nil {
		pos = skipWhitespaceNoNewline(f.data, newPos)
		if _, newPos, err := readInt(f.data, pos); err == nil {
			pos = skipWhitespace(f.data, newPos)
			if f.safeHasPrefix(pos, "obj") {
				pos += len("obj")
				pos = skipWhitespace(f.data, pos)
				if pos+1 < len(f.data) && f.data[pos] == '<' && f.data[pos+1] == '<' {
					// Check if /Type /XRef appears within the immediate next 256 bytes.
					end := pos + 512
					if end > len(f.data) {
						end = len(f.data)
					}
					if bytes.Contains(f.data[pos:end], []byte("/XRef")) {
						return true
					}
				}
			}
		}
	}

	_ = saved
	return false
}

// parseClassicXref parses a classic uncompressed xref table starting at pos
// (which must be at the "xref" keyword) and the following trailer dictionary.
func (f *pdfFile) parseClassicXref(pos int) (pdfDict, int64, error) {
	pos += len("xref")
	pos = skipWhitespaceNoNewline(f.data, pos)
	if pos < len(f.data) && (f.data[pos] == '\n' || f.data[pos] == '\r') {
		pos = skipNewline(f.data, pos)
	}

	for pos < len(f.data) {
		pos = skipWhitespace(f.data, pos)
		if f.safeHasPrefix(pos, "trailer") {
			break
		}
		if !isDigit(f.data[pos]) {
			break
		}

		startObj, newPos, err := readInt(f.data, pos)
		if err != nil {
			return nil, 0, fmt.Errorf("reading xref subsection start: %w", err)
		}
		pos = skipWhitespaceNoNewline(f.data, newPos)

		count, newPos, err := readInt(f.data, pos)
		if err != nil {
			return nil, 0, fmt.Errorf("reading xref subsection count: %w", err)
		}
		pos = newPos
		pos = skipNewline(f.data, pos)

		for i := range count {
			if pos+20 > len(f.data) {
				return nil, 0, fmt.Errorf("xref entry truncated at object %d", startObj+i)
			}

			entry := f.data[pos : pos+20]

			entryOffset, err := strconv.ParseInt(string(bytes.TrimSpace(entry[0:10])), 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("parsing xref entry offset: %w", err)
			}

			inUse := entry[17] == 'n'
			objNum := startObj + i

			if inUse {
				// First-write wins: the xref at startxref takes priority over
				// older sections reached via /Prev.
				if _, exists := f.xref[objNum]; !exists {
					f.xref[objNum] = xrefEntry{kind: xrefUncompressed, offset: entryOffset}
				}
			}

			pos += 20
		}
	}

	pos = skipWhitespace(f.data, pos)
	if !f.safeHasPrefix(pos, "trailer") {
		return nil, 0, fmt.Errorf("expected 'trailer' after xref table")
	}
	pos += len("trailer")
	pos = skipWhitespace(f.data, pos)

	val, _, err := parseValue(f.data, pos, f)
	if err != nil {
		return nil, 0, fmt.Errorf("parsing trailer dict: %w", err)
	}

	trailer, ok := val.(pdfDict)
	if !ok {
		return nil, 0, fmt.Errorf("trailer is not a dictionary")
	}

	var prev int64
	if p, ok := trailer["Prev"].(pdfNumber); ok {
		v, ok := validXrefOffset(p)
		if !ok {
			return nil, 0, fmt.Errorf("invalid /Prev offset %v", float64(p))
		}
		prev = v
	}

	return trailer, prev, nil
}

// resolve dereferences an indirect reference. If v is a pdfRef it looks up and
// parses the object (caching the result). Any other value is returned as-is.
//
// Cycles (an object whose body re-resolves the same object — e.g., a stream
// whose /Length is a self-reference, or a compressed object stream that hosts
// itself) are broken by the inFlight set: re-entry returns nil rather than
// recursing into the unrecoverable stack-overflow case.
func (f *pdfFile) resolve(v any) any {
	ref, ok := v.(pdfRef)
	if !ok {
		return v
	}

	if cached, found := f.cache[ref.num]; found {
		return cached
	}

	if f.inFlight[ref.num] {
		return nil
	}
	if f.inFlight == nil {
		f.inFlight = map[int]bool{}
	}
	f.inFlight[ref.num] = true
	defer delete(f.inFlight, ref.num)

	obj, err := f.readObject(ref.num)
	if err != nil {
		// Callers use comma-ok type assertions; nil is a reasonable sentinel.
		return nil
	}

	f.cache[ref.num] = obj
	return obj
}

// readObject dispatches to the appropriate reader based on the xref entry kind.
func (f *pdfFile) readObject(num int) (any, error) {
	e, ok := f.xref[num]
	if !ok {
		return nil, fmt.Errorf("object %d not in xref table", num)
	}
	switch e.kind {
	case xrefUncompressed:
		return f.readUncompressedObject(num, e.offset)
	case xrefCompressed:
		return f.readCompressedObject(num, e.objStmNum, e.objStmIdx)
	case xrefFree:
		return nil, fmt.Errorf("object %d is a free entry", num)
	}
	return nil, fmt.Errorf("object %d: unknown xref entry kind %d", num, e.kind)
}

// readUncompressedObject parses the indirect object definition at the given
// byte offset.
func (f *pdfFile) readUncompressedObject(num int, off int64) (any, error) {
	pos := int(off)
	pos = skipWhitespace(f.data, pos)

	if _, newPos, err := readInt(f.data, pos); err != nil {
		return nil, fmt.Errorf("reading object number for obj %d: %w", num, err)
	} else {
		pos = skipWhitespaceNoNewline(f.data, newPos)
	}

	if _, newPos, err := readInt(f.data, pos); err != nil {
		return nil, fmt.Errorf("reading generation number for obj %d: %w", num, err)
	} else {
		pos = skipWhitespace(f.data, newPos)
	}

	if !f.safeHasPrefix(pos, "obj") {
		return nil, fmt.Errorf("expected 'obj' keyword for object %d, got %q", num, safePrefix(f.data, pos, 8))
	}
	pos += len("obj")
	pos = skipWhitespace(f.data, pos)

	val, newPos, err := parseValue(f.data, pos, f)
	if err != nil {
		return nil, fmt.Errorf("parsing object %d value: %w", num, err)
	}
	pos = newPos

	// Check for stream body.
	pos = skipWhitespace(f.data, pos)
	if dict, ok := val.(pdfDict); ok && f.safeHasPrefix(pos, "stream") {
		pos += len("stream")
		pos = skipNewline(f.data, pos)

		length := 0
		if lv, exists := dict["Length"]; exists {
			lv = f.resolve(lv)
			if n, ok := lv.(pdfNumber); ok {
				length = int(n)
			}
		}

		if length < 0 {
			return nil, fmt.Errorf("negative stream length %d for object %d", length, num)
		}
		if pos+length > len(f.data) {
			return nil, fmt.Errorf("stream length %d exceeds file size for object %d", length, num)
		}

		streamData := make([]byte, length)
		copy(streamData, f.data[pos:pos+length])

		return pdfStream{dict: dict, data: streamData}, nil
	}

	return val, nil
}
