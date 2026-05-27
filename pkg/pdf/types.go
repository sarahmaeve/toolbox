// Package pdf implements a minimal text extractor for digital PDF documents
// using only the Go standard library. It targets PDF 1.4 through 1.7,
// including PDF 1.5+ features (compressed cross-reference streams and object
// streams) commonly found in government and military publications.
package pdf

// PDF object types. PDF values are represented as Go types:
//
//   - pdfDict:   dictionary (/Key Value pairs)
//   - pdfArray:  ordered collection [...]
//   - pdfName:   /Name
//   - pdfString: (literal) or <hex>
//   - pdfNumber: integer or real
//   - pdfBool:   true/false
//   - pdfRef:    indirect reference (N G R)
//   - pdfNull:   null
//   - pdfStream: stream with dictionary and raw (still-encoded) data
type (
	pdfDict   map[string]any
	pdfArray  []any
	pdfName   string
	pdfString string
	pdfNumber float64
	pdfBool   bool
	pdfNull   struct{}
)

type pdfRef struct{ num, gen int }

type pdfStream struct {
	dict pdfDict
	data []byte // raw (still-filtered) data; call decodeStream to get plaintext
}

// xrefEntryKind classifies a cross-reference entry per PDF 1.5 §7.5.8.
type xrefEntryKind uint8

const (
	xrefFree         xrefEntryKind = 0 // unused slot
	xrefUncompressed xrefEntryKind = 1 // direct: entry.offset is the byte offset in the file
	xrefCompressed   xrefEntryKind = 2 // packed in an object stream: entry.objStm{Num,Idx}
)

// xrefEntry is a single decoded cross-reference table row. Only the fields
// relevant to its kind are populated.
type xrefEntry struct {
	kind      xrefEntryKind
	offset    int64 // file offset (xrefUncompressed)
	objStmNum int   // host object-stream number (xrefCompressed)
	objStmIdx int   // index within that object stream (xrefCompressed)
}

// pdfFile is an opened PDF document held entirely in memory.
type pdfFile struct {
	data    []byte            // entire file contents
	xref    map[int]xrefEntry // object number -> xref entry
	trailer pdfDict           // trailer dictionary (carries /Root, /Info, /Size)
	cache   map[int]any       // resolved object cache (objNum -> parsed value)
	objStms map[int]*objStm   // parsed object streams, keyed by their obj num

	// inFlight tracks objects currently being resolved so a self-referential
	// /Length or a compressed object stream that hosts itself cannot drive
	// resolve into infinite recursion (which would be a fatal stack overflow,
	// not a recoverable panic).
	inFlight map[int]bool
}

// objStm is a parsed PDF 1.5 object stream (/Type /ObjStm). pairs maps the
// position of each contained object to its (objectNumber, offsetWithinBody)
// — offset is relative to body[0], not the original decompressed stream.
type objStm struct {
	body  []byte
	pairs [][2]int
}

// cmapTable maps PDF character codes to Unicode strings. One code may map to
// multiple code points (e.g., ligatures such as "fi").
type cmapTable map[uint16]string
