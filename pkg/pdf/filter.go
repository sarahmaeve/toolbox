package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// decodeStream decompresses and unfilters s according to its /Filter and
// /DecodeParms entries.
func (f *pdfFile) decodeStream(s pdfStream) ([]byte, error) {
	filter := f.resolve(s.dict["Filter"])
	if filter == nil {
		return s.data, nil
	}

	var filters pdfArray
	switch v := filter.(type) {
	case pdfName:
		filters = pdfArray{v}
	case pdfArray:
		filters = v
	default:
		return nil, fmt.Errorf("unexpected /Filter type %T", filter)
	}

	// /DecodeParms parallels /Filter; may be a dict or array of dicts.
	parms := f.resolve(s.dict["DecodeParms"])
	parmsAt := func(i int) pdfDict {
		switch p := parms.(type) {
		case pdfDict:
			if i == 0 {
				return p
			}
		case pdfArray:
			if i < len(p) {
				return f.getDict(p[i])
			}
		}
		return nil
	}

	result := s.data
	for i, fv := range filters {
		name := f.getName(fv)
		switch name {
		case "FlateDecode":
			decoded, err := decompressFlate(result)
			if err != nil {
				return nil, err
			}
			if p := parmsAt(i); p != nil {
				decoded, err = applyPredictor(decoded, p)
				if err != nil {
					return nil, fmt.Errorf("FlateDecode predictor: %w", err)
				}
			}
			result = decoded
		case "":
			// No filter — pass through.
		default:
			return nil, fmt.Errorf("unsupported stream filter: %q", name)
		}
	}

	return result, nil
}

// decompressFlate decompresses a zlib/deflate-compressed byte slice.
func decompressFlate(data []byte) ([]byte, error) {
	rc, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("zlib init: %w", err)
	}
	defer rc.Close()
	decoded, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("flate decompress: %w", err)
	}
	return decoded, nil
}

// --- Helper accessors --------------------------------------------------------

// getDict resolves v and returns it as a pdfDict. Returns nil on type mismatch.
func (f *pdfFile) getDict(v any) pdfDict {
	d, _ := f.resolve(v).(pdfDict)
	return d
}

// getArray resolves v and returns it as a pdfArray. Returns nil on type mismatch.
func (f *pdfFile) getArray(v any) pdfArray {
	a, _ := f.resolve(v).(pdfArray)
	return a
}

// getName resolves v and returns the underlying name. Returns "" on type mismatch.
func (f *pdfFile) getName(v any) string {
	n, _ := f.resolve(v).(pdfName)
	return string(n)
}

// getStream resolves v and returns a *pdfStream and true on success.
func (f *pdfFile) getStream(v any) (*pdfStream, bool) {
	resolved := f.resolve(v)
	if resolved == nil {
		return nil, false
	}
	s, ok := resolved.(pdfStream)
	if !ok {
		return nil, false
	}
	return &s, true
}

// getInt resolves v and returns it as an int with a default. Returns dflt if
// the value is missing or not numeric.
func (f *pdfFile) getInt(v any, dflt int) int {
	resolved := f.resolve(v)
	if n, ok := resolved.(pdfNumber); ok {
		return int(n)
	}
	return dflt
}
