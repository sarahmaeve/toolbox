package pdf

import (
	"bytes"
	"fmt"
	"strconv"
)

// maxParseDepth caps recursion through parseValue/parseDict/parseArray so a
// hostile PDF nesting `[[[[...]]]]` thousands of levels deep produces an
// error instead of hitting Go's stack-growth ceiling (a fatal, unrecoverable
// panic). Real-world PDFs nest in the low double digits.
const maxParseDepth = 256

// parseValue parses a single PDF value starting at pos in data.
// It returns the parsed value and the position immediately after it.
func parseValue(data []byte, pos int, f *pdfFile) (any, int, error) {
	return parseValueDepth(data, pos, f, 0)
}

func parseValueDepth(data []byte, pos int, f *pdfFile, depth int) (any, int, error) {
	if depth > maxParseDepth {
		return nil, pos, fmt.Errorf("parser: exceeded max nesting depth %d at offset %d", maxParseDepth, pos)
	}
	pos = skipWhitespaceAndComments(data, pos)

	if pos >= len(data) {
		return nil, pos, fmt.Errorf("unexpected end of data")
	}

	ch := data[pos]

	switch {
	case ch == '<' && pos+1 < len(data) && data[pos+1] == '<':
		return parseDict(data, pos, f, depth+1)

	case ch == '<':
		return parseHexString(data, pos)

	case ch == '(':
		return parseLiteralString(data, pos)

	case ch == '[':
		return parseArray(data, pos, f, depth+1)

	case ch == '/':
		return parseName(data, pos)

	case ch == 't' && bytes.HasPrefix(data[pos:], []byte("true")):
		return pdfBool(true), pos + len("true"), nil

	case ch == 'f' && bytes.HasPrefix(data[pos:], []byte("false")):
		return pdfBool(false), pos + len("false"), nil

	case ch == 'n' && bytes.HasPrefix(data[pos:], []byte("null")):
		return pdfNull{}, pos + len("null"), nil

	case ch == '+' || ch == '-' || ch == '.' || isDigit(ch):
		return parseNumberOrRef(data, pos, f, depth)

	default:
		return nil, pos, fmt.Errorf("unexpected character %q at offset %d", ch, pos)
	}
}

// parseDict parses a PDF dictionary << /Key Value ... >>.
func parseDict(data []byte, pos int, f *pdfFile, depth int) (pdfDict, int, error) {
	pos += 2 // skip <<
	dict := pdfDict{}

	for {
		pos = skipWhitespaceAndComments(data, pos)
		if pos >= len(data) {
			return nil, pos, fmt.Errorf("unterminated dictionary")
		}
		if data[pos] == '>' && pos+1 < len(data) && data[pos+1] == '>' {
			return dict, pos + 2, nil
		}

		if data[pos] != '/' {
			return nil, pos, fmt.Errorf("expected name key in dictionary, got %q", data[pos])
		}
		keyVal, newPos, err := parseName(data, pos)
		if err != nil {
			return nil, pos, fmt.Errorf("parsing dictionary key: %w", err)
		}
		pos = newPos
		key := string(keyVal)

		pos = skipWhitespaceAndComments(data, pos)

		val, newPos, err := parseValueDepth(data, pos, f, depth)
		if err != nil {
			return nil, pos, fmt.Errorf("parsing dictionary value for key %q: %w", key, err)
		}
		pos = newPos

		dict[key] = val
	}
}

// parseArray parses a PDF array [ ... ].
func parseArray(data []byte, pos int, f *pdfFile, depth int) (pdfArray, int, error) {
	pos++ // skip [
	arr := pdfArray{}

	for {
		pos = skipWhitespaceAndComments(data, pos)
		if pos >= len(data) {
			return nil, pos, fmt.Errorf("unterminated array")
		}
		if data[pos] == ']' {
			return arr, pos + 1, nil
		}

		val, newPos, err := parseValueDepth(data, pos, f, depth)
		if err != nil {
			return nil, pos, fmt.Errorf("parsing array element: %w", err)
		}
		pos = newPos
		arr = append(arr, val)
	}
}

// parseName parses a PDF name /SomeName.
func parseName(data []byte, pos int) (pdfName, int, error) {
	pos++ // skip /
	start := pos
	for pos < len(data) && !isDelimiter(data[pos]) && !isWhitespace(data[pos]) {
		pos++
	}
	return pdfName(data[start:pos]), pos, nil
}

// parseLiteralString parses a PDF literal string (text with balanced parens).
func parseLiteralString(data []byte, pos int) (pdfString, int, error) {
	pos++ // skip (
	var buf bytes.Buffer
	depth := 1

	for pos < len(data) {
		ch := data[pos]

		switch {
		case ch == '\\' && pos+1 < len(data):
			pos++
			esc := data[pos]
			switch esc {
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			case 'b':
				buf.WriteByte('\b')
			case 'f':
				buf.WriteByte('\f')
			case '(':
				buf.WriteByte('(')
			case ')':
				buf.WriteByte(')')
			case '\\':
				buf.WriteByte('\\')
			case '\r':
				if pos+1 < len(data) && data[pos+1] == '\n' {
					pos++
				}
			case '\n':
				// Line continuation.
			default:
				if esc >= '0' && esc <= '7' {
					octal := []byte{esc}
					for range 2 {
						if pos+1 >= len(data) || data[pos+1] < '0' || data[pos+1] > '7' {
							break
						}
						pos++
						octal = append(octal, data[pos])
					}
					n, _ := strconv.ParseUint(string(octal), 8, 8)
					buf.WriteByte(byte(n))
				} else {
					buf.WriteByte(esc)
				}
			}
			pos++

		case ch == '(':
			depth++
			buf.WriteByte(ch)
			pos++

		case ch == ')':
			depth--
			if depth == 0 {
				return pdfString(buf.String()), pos + 1, nil
			}
			buf.WriteByte(ch)
			pos++

		default:
			buf.WriteByte(ch)
			pos++
		}
	}

	return "", pos, fmt.Errorf("unterminated literal string")
}

// parseHexString parses a PDF hex string <4865...>.
func parseHexString(data []byte, pos int) (pdfString, int, error) {
	pos++ // skip <
	var buf bytes.Buffer

	for pos < len(data) {
		if data[pos] == '>' {
			return pdfString(buf.String()), pos + 1, nil
		}

		if isWhitespace(data[pos]) {
			pos++
			continue
		}

		hi := hexNibble(data[pos])
		pos++

		lo := byte(0)
		if pos < len(data) && data[pos] != '>' && !isWhitespace(data[pos]) {
			lo = hexNibble(data[pos])
			pos++
		}

		buf.WriteByte(hi<<4 | lo)
	}

	return "", pos, fmt.Errorf("unterminated hex string")
}

// parseNumberOrRef parses an integer, float, or indirect reference (N G R).
// It peeks ahead to distinguish bare numbers from references.
func parseNumberOrRef(data []byte, pos int, f *pdfFile, depth int) (any, int, error) {
	n1Str, newPos, err := readRawNumber(data, pos)
	if err != nil {
		return nil, pos, fmt.Errorf("parsing number: %w", err)
	}

	if isInteger(n1Str) {
		saved := newPos
		peek := skipWhitespaceAndComments(data, saved)

		if peek < len(data) && isDigit(data[peek]) {
			n2Str, peek2, err2 := readRawNumber(data, peek)
			if err2 == nil && isInteger(n2Str) {
				peek2 = skipWhitespaceAndComments(data, peek2)

				if bytes.HasPrefix(data[peek2:], []byte("R")) && isTokenEnd(data, peek2+1) {
					n1, _ := strconv.Atoi(n1Str)
					n2, _ := strconv.Atoi(n2Str)
					return pdfRef{num: n1, gen: n2}, peek2 + 1, nil
				}

				if bytes.HasPrefix(data[peek2:], []byte("obj")) && isTokenEnd(data, peek2+3) {
					peek2 += 3
					peek2 = skipWhitespace(data, peek2)
					val, afterVal, err3 := parseValueDepth(data, peek2, f, depth+1)
					if err3 != nil {
						return nil, pos, fmt.Errorf("parsing inline object: %w", err3)
					}
					afterVal = skipWhitespace(data, afterVal)
					if bytes.HasPrefix(data[afterVal:], []byte("endobj")) {
						afterVal += len("endobj")
					}
					return val, afterVal, nil
				}
			}
		}
	}

	if bytes.ContainsAny([]byte(n1Str), ".") {
		f64, err := strconv.ParseFloat(n1Str, 64)
		if err != nil {
			return nil, pos, fmt.Errorf("parsing float %q: %w", n1Str, err)
		}
		return pdfNumber(f64), newPos, nil
	}

	i64, err := strconv.ParseInt(n1Str, 10, 64)
	if err != nil {
		return nil, pos, fmt.Errorf("parsing integer %q: %w", n1Str, err)
	}
	return pdfNumber(i64), newPos, nil
}

// --- Low-level lexer utilities -----------------------------------------------

// skipWhitespace advances pos past all whitespace including newlines.
func skipWhitespace(data []byte, pos int) int {
	for pos < len(data) && isWhitespace(data[pos]) {
		pos++
	}
	return pos
}

// skipWhitespaceNoNewline advances pos past spaces and tabs only.
func skipWhitespaceNoNewline(data []byte, pos int) int {
	for pos < len(data) && (data[pos] == ' ' || data[pos] == '\t') {
		pos++
	}
	return pos
}

// skipWhitespaceAndComments advances pos past whitespace and % comment lines.
func skipWhitespaceAndComments(data []byte, pos int) int {
	for pos < len(data) {
		if isWhitespace(data[pos]) {
			pos++
			continue
		}
		if data[pos] == '%' {
			for pos < len(data) && data[pos] != '\n' && data[pos] != '\r' {
				pos++
			}
			continue
		}
		break
	}
	return pos
}

// skipNewline skips one line ending (\r\n, \r, or \n).
func skipNewline(data []byte, pos int) int {
	if pos >= len(data) {
		return pos
	}
	if data[pos] == '\r' {
		pos++
		if pos < len(data) && data[pos] == '\n' {
			pos++
		}
		return pos
	}
	if data[pos] == '\n' {
		return pos + 1
	}
	return pos
}

// readInt reads a decimal integer from data starting at pos.
func readInt(data []byte, pos int) (int, int, error) {
	start := pos
	if pos < len(data) && (data[pos] == '+' || data[pos] == '-') {
		pos++
	}
	if pos >= len(data) || !isDigit(data[pos]) {
		return 0, start, fmt.Errorf("expected integer at offset %d", start)
	}
	for pos < len(data) && isDigit(data[pos]) {
		pos++
	}
	n, err := strconv.Atoi(string(data[start:pos]))
	if err != nil {
		return 0, start, fmt.Errorf("parsing integer %q: %w", data[start:pos], err)
	}
	return n, pos, nil
}

// readRawNumber reads the raw bytes of a number token (integer or float).
func readRawNumber(data []byte, pos int) (string, int, error) {
	start := pos
	if pos < len(data) && (data[pos] == '+' || data[pos] == '-') {
		pos++
	}
	hasContent := false
	for pos < len(data) && (isDigit(data[pos]) || data[pos] == '.') {
		hasContent = true
		pos++
	}
	if !hasContent {
		return "", start, fmt.Errorf("expected number at offset %d", start)
	}
	return string(data[start:pos]), pos, nil
}

// isTokenEnd returns true if pos is at a delimiter, whitespace, or end of data.
func isTokenEnd(data []byte, pos int) bool {
	if pos >= len(data) {
		return true
	}
	return isWhitespace(data[pos]) || isDelimiter(data[pos])
}

// isInteger returns true if s is an optional sign followed by digits.
func isInteger(s string) bool {
	if len(s) == 0 {
		return false
	}
	start := 0
	if s[0] == '+' || s[0] == '-' {
		start = 1
	}
	if start >= len(s) {
		return false
	}
	for _, ch := range []byte(s[start:]) {
		if !isDigit(ch) {
			return false
		}
	}
	return true
}

func hexNibble(ch byte) byte {
	switch {
	case ch >= '0' && ch <= '9':
		return ch - '0'
	case ch >= 'a' && ch <= 'f':
		return ch - 'a' + 10
	case ch >= 'A' && ch <= 'F':
		return ch - 'A' + 10
	default:
		return 0
	}
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == 0
}

func isDelimiter(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// safeHasPrefix reports whether f.data[pos:] starts with prefix, returning
// false (rather than panicking) when pos is out of bounds.
func (f *pdfFile) safeHasPrefix(pos int, prefix string) bool {
	if pos < 0 || pos >= len(f.data) {
		return false
	}
	return bytes.HasPrefix(f.data[pos:], []byte(prefix))
}

// safePrefix returns up to n bytes from data[pos:] as a string for diagnostics.
func safePrefix(data []byte, pos, n int) string {
	end := pos + n
	if end > len(data) {
		end = len(data)
	}
	return string(data[pos:end])
}
