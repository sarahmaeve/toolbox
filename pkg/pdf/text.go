package pdf

import (
	"encoding/hex"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// extractText parses a decompressed PDF content stream and returns the plain
// text it contains. fonts maps font resource names (without the leading "/")
// to their CMap tables for character decoding.
func extractText(content []byte, fonts map[string]cmapTable) string {
	p := &contentParser{
		data:  content,
		fonts: fonts,
	}
	return p.parse()
}

// contentParser holds mutable state for one content stream parse.
type contentParser struct {
	data  []byte
	fonts map[string]cmapTable

	operands []token

	inText      bool
	currentFont string
	yPos        float64
	ySet        bool

	out      strings.Builder
	lastRune rune
}

// token is a single value parsed from the content stream.
type token struct {
	kind tokenKind
	s    string  // string/name/operator value
	n    float64 // numeric value
	arr  []token // array elements (kind == tokArray)
}

type tokenKind int

const (
	tokNumber tokenKind = iota
	tokString
	tokName
	tokOperator
	tokArray
)

// --- Top-level parse loop ----------------------------------------------------

func (p *contentParser) parse() string {
	pos := 0

	for pos < len(p.data) {
		pos = skipContentWS(p.data, pos)
		if pos >= len(p.data) {
			break
		}

		tok, next, ok := readToken(p.data, pos)
		if !ok {
			pos++
			continue
		}
		pos = next

		if tok.kind == tokOperator {
			p.execute(tok.s)
			p.operands = p.operands[:0]
		} else {
			p.operands = append(p.operands, tok)
		}
	}

	return normalizeText(p.out.String())
}

// --- Operator execution ------------------------------------------------------

func (p *contentParser) execute(op string) {
	switch op {
	case "BT":
		p.inText = true
		p.ySet = false

	case "ET":
		p.inText = false
		p.writeNewline()

	case "Tf":
		p.execTf()

	case "Tj":
		p.execTj()

	case "TJ":
		p.execTJ()

	case "'":
		p.writeNewline()
		p.execTj()

	case "\"":
		// word-spacing char-spacing string "
		p.writeNewline()
		p.execTj()

	case "Tm":
		p.execTm()

	case "Td", "TD":
		p.execTd()

	case "T*":
		p.writeNewline()
	}
}

func (p *contentParser) execTf() {
	if len(p.operands) < 2 {
		return
	}
	nameTok := p.operands[len(p.operands)-2]
	if nameTok.kind != tokName {
		return
	}
	name := nameTok.s
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	p.currentFont = name
}

func (p *contentParser) execTj() {
	if len(p.operands) < 1 {
		return
	}
	tok := p.operands[len(p.operands)-1]
	if tok.kind != tokString {
		return
	}
	p.writeString(tok.s)
}

func (p *contentParser) execTJ() {
	if len(p.operands) < 1 {
		return
	}
	tok := p.operands[len(p.operands)-1]
	if tok.kind != tokArray {
		return
	}
	for _, elem := range tok.arr {
		switch elem.kind {
		case tokString:
			p.writeString(elem.s)
		case tokNumber:
			// Large negative numbers represent word-spacing gaps.
			if elem.n < -100 {
				p.writeSpace()
			}
		}
	}
}

func (p *contentParser) execTm() {
	if len(p.operands) < 6 {
		return
	}
	y := p.operands[5].n
	if p.ySet && math.Abs(y-p.yPos) > 1.0 {
		p.writeNewline()
	}
	p.yPos = y
	p.ySet = true
}

func (p *contentParser) execTd() {
	if len(p.operands) < 2 {
		return
	}
	ty := p.operands[1].n
	if ty != 0 {
		p.writeNewline()
		if p.ySet {
			p.yPos += ty
		}
	}
}

// --- Output helpers ----------------------------------------------------------

func (p *contentParser) writeNewline() {
	if p.lastRune == '\n' {
		return
	}
	p.out.WriteByte('\n')
	p.lastRune = '\n'
}

func (p *contentParser) writeSpace() {
	if p.lastRune == ' ' || p.lastRune == '\n' {
		return
	}
	p.out.WriteByte(' ')
	p.lastRune = ' '
}

// writeString decodes a raw PDF string using the current font's CMap.
func (p *contentParser) writeString(s string) {
	cmap := p.fonts[p.currentFont]

	i := 0
	for i < len(s) {
		var mapped string
		var found bool

		if i+1 < len(s) && cmap != nil {
			code := uint16(s[i])<<8 | uint16(s[i+1])
			mapped, found = cmap[code]
		}

		if found {
			p.out.WriteString(mapped)
			if len(mapped) > 0 {
				r, _ := utf8.DecodeLastRuneInString(mapped)
				p.lastRune = r
			}
			i += 2
			continue
		}

		code := uint16(s[i])
		if cmap != nil {
			mapped, found = cmap[code]
		}
		if found {
			p.out.WriteString(mapped)
			if len(mapped) > 0 {
				r, _ := utf8.DecodeLastRuneInString(mapped)
				p.lastRune = r
			}
		} else {
			// Latin-1 fallback: byte value as Unicode code point.
			r := rune(s[i])
			p.out.WriteRune(r)
			p.lastRune = r
		}
		i++
	}
}

// --- Tokenizer ---------------------------------------------------------------

// skipContentWS skips whitespace and % comments inside a content stream.
func skipContentWS(data []byte, pos int) int {
	for pos < len(data) {
		b := data[pos]
		if b == '%' {
			for pos < len(data) && data[pos] != '\n' && data[pos] != '\r' {
				pos++
			}
			continue
		}
		if isWhitespace(b) {
			pos++
			continue
		}
		break
	}
	return pos
}

// readToken reads one token from data starting at pos.
func readToken(data []byte, pos int) (token, int, bool) {
	if pos >= len(data) {
		return token{}, pos, false
	}

	b := data[pos]

	switch {
	case b == '(':
		s, next := readLiteralString(data, pos)
		return token{kind: tokString, s: s}, next, true

	case b == '<' && pos+1 < len(data) && data[pos+1] != '<':
		s, next := readHexString(data, pos)
		return token{kind: tokString, s: s}, next, true

	case b == '/':
		s, next := readName(data, pos)
		return token{kind: tokName, s: s}, next, true

	case b == '[':
		arr, next := readArray(data, pos)
		return token{kind: tokArray, arr: arr}, next, true

	case b == '-' || b == '+' || (b >= '0' && b <= '9') || b == '.':
		n, next, ok := readNumber(data, pos)
		if ok {
			return token{kind: tokNumber, n: n}, next, true
		}
		fallthrough

	default:
		s, next := readOperator(data, pos)
		if s == "" {
			return token{}, next, false
		}
		return token{kind: tokOperator, s: s}, next, true
	}
}

func readLiteralString(data []byte, pos int) (string, int) {
	pos++
	var buf strings.Builder
	depth := 1

	for pos < len(data) && depth > 0 {
		b := data[pos]
		switch b {
		case '(':
			depth++
			buf.WriteByte(b)
			pos++
		case ')':
			depth--
			if depth > 0 {
				buf.WriteByte(b)
			}
			pos++
		case '\\':
			pos++
			if pos >= len(data) {
				break
			}
			esc := data[pos]
			pos++
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
			case '\n', '\r':
				if esc == '\r' && pos < len(data) && data[pos] == '\n' {
					pos++
				}
			default:
				if esc >= '0' && esc <= '7' {
					octal := int(esc - '0')
					for range 2 {
						if pos >= len(data) || data[pos] < '0' || data[pos] > '7' {
							break
						}
						octal = octal*8 + int(data[pos]-'0')
						pos++
					}
					buf.WriteByte(byte(octal))
				} else {
					buf.WriteByte(esc)
				}
			}
		default:
			buf.WriteByte(b)
			pos++
		}
	}

	return buf.String(), pos
}

func readHexString(data []byte, pos int) (string, int) {
	pos++
	var hexBuf strings.Builder
	for pos < len(data) && data[pos] != '>' {
		b := data[pos]
		if isHexDigit(b) {
			hexBuf.WriteByte(b)
		}
		pos++
	}
	if pos < len(data) {
		pos++
	}

	hexStr := hexBuf.String()
	if len(hexStr)%2 != 0 {
		hexStr += "0"
	}
	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return "", pos
	}
	return string(decoded), pos
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func readName(data []byte, pos int) (string, int) {
	start := pos
	pos++ // skip /
	for pos < len(data) && !isDelimiter(data[pos]) && !isWhitespace(data[pos]) {
		pos++
	}
	return string(data[start:pos]), pos
}

func readArray(data []byte, pos int) ([]token, int) {
	pos++ // skip [
	var elems []token

	for pos < len(data) {
		pos = skipContentWS(data, pos)
		if pos >= len(data) {
			break
		}
		if data[pos] == ']' {
			pos++
			break
		}
		tok, next, ok := readToken(data, pos)
		if !ok {
			pos++
			continue
		}
		pos = next
		if tok.kind == tokString || tok.kind == tokNumber {
			elems = append(elems, tok)
		}
	}

	return elems, pos
}

func readNumber(data []byte, pos int) (float64, int, bool) {
	start := pos
	if pos < len(data) && (data[pos] == '+' || data[pos] == '-') {
		pos++
	}
	hasDigit := false
	for pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
		hasDigit = true
		pos++
	}
	if pos < len(data) && data[pos] == '.' {
		pos++
		for pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
			hasDigit = true
			pos++
		}
	}
	if !hasDigit {
		return 0, start, false
	}
	if pos < len(data) && !isDelimiter(data[pos]) && !isWhitespace(data[pos]) {
		return 0, start, false
	}
	return parseSimpleFloat(data[start:pos]), pos, true
}

// parseSimpleFloat parses ASCII bytes to float64 without importing strconv.
func parseSimpleFloat(b []byte) float64 {
	neg := false
	i := 0
	if i < len(b) && b[i] == '-' {
		neg = true
		i++
	} else if i < len(b) && b[i] == '+' {
		i++
	}
	var intPart float64
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		intPart = intPart*10 + float64(b[i]-'0')
		i++
	}
	var fracPart float64
	if i < len(b) && b[i] == '.' {
		i++
		scale := 0.1
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			fracPart += float64(b[i]-'0') * scale
			scale *= 0.1
			i++
		}
	}
	v := intPart + fracPart
	if neg {
		v = -v
	}
	return v
}

func readOperator(data []byte, pos int) (string, int) {
	start := pos
	for pos < len(data) && !isDelimiter(data[pos]) && !isWhitespace(data[pos]) {
		pos++
	}
	return string(data[start:pos]), pos
}

// --- Post-processing ---------------------------------------------------------

// normalizeText collapses redundant whitespace and trims line edges. It does
// NOT try to rejoin broken-up text lines — content-stream output from a real
// PDF is rich enough that aggressive re-joining tends to corrupt prose.
func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		line = collapseSpaces(line)
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		out = append(out, line)
	}

	result := strings.Join(out, "\n")
	return strings.Trim(result, "\n")
}

// collapseSpaces reduces any run of space/tab characters to a single space.
func collapseSpaces(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				buf.WriteByte(' ')
			}
			prevSpace = true
		} else {
			buf.WriteRune(r)
			prevSpace = false
		}
	}
	return buf.String()
}
