package pdf

import (
	"fmt"
	"math"
)

// parseXrefStreamAt parses an indirect object at offset whose stream is a
// cross-reference stream (/Type /XRef per PDF 1.5 §7.5.8). The stream's own
// dictionary serves as the trailer. Returns the trailer dict and the /Prev
// offset (0 if absent).
func (f *pdfFile) parseXrefStreamAt(offset int) (pdfDict, int64, error) {
	// The indirect object header (N G obj) is not yet in the xref map. Parse
	// it directly from the raw byte offset. readUncompressedObject uses the
	// object number only for error messages, so passing 0 is fine here.
	obj, err := f.readUncompressedObject(0, int64(offset))
	if err != nil {
		return nil, 0, fmt.Errorf("reading xref-stream object at %d: %w", offset, err)
	}

	stream, ok := obj.(pdfStream)
	if !ok {
		return nil, 0, fmt.Errorf("object at %d is not a stream (got %T)", offset, obj)
	}

	dict := stream.dict
	if t, _ := dict["Type"].(pdfName); t != "XRef" {
		return nil, 0, fmt.Errorf("xref stream missing /Type /XRef (got %q)", t)
	}

	// /W [w1 w2 w3]: byte widths for each entry field.
	wArr, ok := dict["W"].(pdfArray)
	if !ok || len(wArr) < 3 {
		return nil, 0, fmt.Errorf("xref stream missing or malformed /W")
	}
	var w [3]int
	for i := range 3 {
		n, ok := wArr[i].(pdfNumber)
		if !ok {
			return nil, 0, fmt.Errorf("/W[%d] is not a number (got %T)", i, wArr[i])
		}
		w[i] = int(n)
	}

	// /Index [s1 c1 s2 c2 ...]. Defaults to [0 /Size] when absent.
	size := f.getInt(dict["Size"], 0)
	var index [][2]int
	if raw, ok := dict["Index"].(pdfArray); ok {
		if len(raw)%2 != 0 {
			return nil, 0, fmt.Errorf("/Index has odd length %d", len(raw))
		}
		for i := 0; i < len(raw); i += 2 {
			s, ok1 := raw[i].(pdfNumber)
			c, ok2 := raw[i+1].(pdfNumber)
			if !ok1 || !ok2 {
				return nil, 0, fmt.Errorf("/Index pair %d not numeric", i/2)
			}
			index = append(index, [2]int{int(s), int(c)})
		}
	} else {
		index = [][2]int{{0, size}}
	}

	decoded, err := f.decodeStream(stream)
	if err != nil {
		return nil, 0, fmt.Errorf("decoding xref-stream contents: %w", err)
	}

	if err := decodeXrefStreamEntries(decoded, w, index, f.xref); err != nil {
		return nil, 0, fmt.Errorf("decoding xref-stream entries: %w", err)
	}

	var prev int64
	if p, ok := dict["Prev"].(pdfNumber); ok {
		v, ok := validXrefOffset(p)
		if !ok {
			return nil, 0, fmt.Errorf("invalid /Prev offset %v in xref-stream", float64(p))
		}
		prev = v
	}

	return dict, prev, nil
}

// decodeXrefStreamEntries walks the binary cross-reference entries in data,
// grouped by subsections from /Index, and populates into. Each entry is
// w[0]+w[1]+w[2] bytes. First-write-wins is enforced so /Prev sections do
// not overwrite newer entries.
//
// Entry kinds (PDF 1.5 §7.5.8.3 Table 18):
//
//	0  free object  — skipped
//	1  uncompressed — field2 is byte offset, field3 is generation
//	2  compressed   — field2 is host objstm number, field3 is index within it
//
// When w[0] == 0 the type defaults to 1 per spec.
func decodeXrefStreamEntries(data []byte, w [3]int, index [][2]int, into map[int]xrefEntry) error {
	entryLen := w[0] + w[1] + w[2]
	if entryLen == 0 {
		return fmt.Errorf("xref-stream /W widths all zero")
	}

	pos := 0
	for _, sub := range index {
		startObj, count := sub[0], sub[1]
		for i := range count {
			if pos+entryLen > len(data) {
				return fmt.Errorf("xref-stream truncated: need %d bytes at offset %d (have %d)",
					entryLen, pos, len(data))
			}

			var fields [3]uint64
			off := pos
			for k := range 3 {
				if w[k] == 0 {
					fields[k] = 0
				} else {
					fields[k] = readBE(data[off : off+w[k]])
				}
				off += w[k]
			}

			entryType := fields[0]
			if w[0] == 0 {
				entryType = 1
			}

			objNum := startObj + i
			pos += entryLen

			// First-write-wins guard: don't overwrite entries from a newer
			// section reached first via the startxref offset.
			if _, exists := into[objNum]; exists {
				continue
			}

			switch entryType {
			case 0:
				// Free object — not recorded.
			case 1:
				if fields[1] > math.MaxInt64 {
					return fmt.Errorf("xref-stream entry %d: offset %d exceeds int64 range", objNum, fields[1])
				}
				into[objNum] = xrefEntry{
					kind:   xrefUncompressed,
					offset: int64(fields[1]),
				}
			case 2:
				if fields[1] > math.MaxInt32 || fields[2] > math.MaxInt32 {
					return fmt.Errorf("xref-stream entry %d: compressed-objstm fields %d/%d exceed int32 range", objNum, fields[1], fields[2])
				}
				into[objNum] = xrefEntry{
					kind:      xrefCompressed,
					objStmNum: int(fields[1]),
					objStmIdx: int(fields[2]),
				}
			default:
				// Unknown types are ignored per spec (forward-compatible).
			}
		}
	}

	return nil
}

// readBE reads up to 8 bytes from data as a big-endian unsigned integer.
func readBE(data []byte) uint64 {
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v
}
