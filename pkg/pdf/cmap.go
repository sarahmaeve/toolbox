package pdf

import (
	"encoding/hex"
	"strings"
	"unicode/utf16"
)

// parseCMap parses a ToUnicode CMap from raw (decompressed) stream data and
// returns a mapping from character codes to Unicode strings.
//
// It handles both bfchar (direct mappings) and bfrange (range mappings),
// including multi-codepoint destinations such as ligatures ("fi", "fl", etc.).
func parseCMap(data []byte) cmapTable {
	table := cmapTable{}
	s := string(data)

	parseBfcharSections(s, table)
	parseBfrangeSections(s, table)

	return table
}

// parseBfcharSections finds every beginbfchar…endbfchar block and adds its
// entries to table.
func parseBfcharSections(s string, table cmapTable) {
	const (
		begin = "beginbfchar"
		end   = "endbfchar"
	)

	for {
		startIdx := strings.Index(s, begin)
		if startIdx == -1 {
			break
		}
		s = s[startIdx+len(begin):]

		endIdx := strings.Index(s, end)
		if endIdx == -1 {
			break
		}
		block := s[:endIdx]
		s = s[endIdx+len(end):]

		tokens := hexTokens(block)
		for i := 0; i+1 < len(tokens); i += 2 {
			src, ok := parseCharCode(tokens[i])
			if !ok {
				continue
			}
			dst := parseUnicodeString(tokens[i+1])
			if dst != "" {
				table[src] = dst
			}
		}
	}
}

// parseBfrangeSections finds every beginbfrange…endbfrange block and adds its
// entries to table.
func parseBfrangeSections(s string, table cmapTable) {
	const (
		begin = "beginbfrange"
		end   = "endbfrange"
	)

	for {
		startIdx := strings.Index(s, begin)
		if startIdx == -1 {
			break
		}
		s = s[startIdx+len(begin):]

		endIdx := strings.Index(s, end)
		if endIdx == -1 {
			break
		}
		block := s[:endIdx]
		s = s[endIdx+len(end):]

		parseBfrangeBlock(block, table)
	}
}

// parseBfrangeBlock processes a single bfrange body. Two forms:
//
//	<start> <end> <dstStart>        — arithmetic range
//	<start> <end> [<d0> <d1> …]    — array form
func parseBfrangeBlock(block string, table cmapTable) {
	tokens := rangeTokens(block)

	for i := 0; i+2 < len(tokens); i += 3 {
		startCode, ok := parseCharCode(tokens[i])
		if !ok {
			continue
		}
		endCode, ok := parseCharCode(tokens[i+1])
		if !ok {
			continue
		}
		third := tokens[i+2]

		if strings.HasPrefix(third, "[") {
			inner := strings.TrimPrefix(third, "[")
			inner = strings.TrimSuffix(inner, "]")
			dstTokens := hexTokens(inner)
			for j := uint16(0); j <= uint16(endCode-startCode) && int(j) < len(dstTokens); j++ {
				code := startCode + j
				dst := parseUnicodeString(dstTokens[j])
				if dst != "" {
					table[code] = dst
				}
			}
		} else {
			baseRune, ok := parseFirstRune(third)
			if !ok {
				continue
			}
			count := int(endCode) - int(startCode) + 1
			for k := range count {
				code := startCode + uint16(k)
				offset := rune(k)
				table[code] = string(baseRune + offset)
			}
		}
	}
}

// hexTokens extracts all <…> hex tokens from s.
func hexTokens(s string) []string {
	var tokens []string
	for {
		open := strings.IndexByte(s, '<')
		if open == -1 {
			break
		}
		closeIdx := strings.IndexByte(s[open:], '>')
		if closeIdx == -1 {
			break
		}
		tokens = append(tokens, s[open:open+closeIdx+1])
		s = s[open+closeIdx+1:]
	}
	return tokens
}

// rangeTokens extracts tokens from a bfrange body. Tokens are either <…> hex
// values or [<…> <…> …] arrays (returned as a single bracketed token).
func rangeTokens(s string) []string {
	var tokens []string
	for i := 0; i < len(s); {
		switch s[i] {
		case '<':
			closeIdx := strings.IndexByte(s[i:], '>')
			if closeIdx == -1 {
				return tokens
			}
			tokens = append(tokens, s[i:i+closeIdx+1])
			i += closeIdx + 1
		case '[':
			closeIdx := strings.IndexByte(s[i:], ']')
			if closeIdx == -1 {
				return tokens
			}
			tokens = append(tokens, s[i:i+closeIdx+1])
			i += closeIdx + 1
		default:
			i++
		}
	}
	return tokens
}

// parseCharCode converts a hex token like <20> or <0020> to a uint16.
func parseCharCode(token string) (uint16, bool) {
	raw := stripAngles(token)
	if raw == "" || len(raw) > 4 {
		return 0, false
	}
	if len(raw)%2 != 0 {
		raw = "0" + raw
	}
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) == 0 || len(b) > 2 {
		return 0, false
	}
	var code uint16
	for _, by := range b {
		code = code<<8 | uint16(by)
	}
	return code, true
}

// parseUnicodeString converts a hex token representing one or more Unicode
// codepoints into a Go string. Each 4-hex-digit group is one codepoint; 2-digit
// tokens are treated as a single BMP codepoint. Surrogate pairs decode via
// utf16.DecodeRune.
func parseUnicodeString(token string) string {
	raw := stripAngles(token)
	if raw == "" {
		return ""
	}
	if len(raw)%2 != 0 {
		raw = "0" + raw
	}
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) == 0 {
		return ""
	}
	if len(b) == 1 {
		return string(rune(b[0]))
	}
	u16 := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u16 = append(u16, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u16))
}

// parseFirstRune returns the first Unicode rune from a hex token.
func parseFirstRune(token string) (rune, bool) {
	s := parseUnicodeString(token)
	if s == "" {
		return 0, false
	}
	r := []rune(s)
	return r[0], true
}

// stripAngles removes the leading '<' and trailing '>' from a hex token.
func stripAngles(token string) string {
	token = strings.TrimPrefix(token, "<")
	token = strings.TrimSuffix(token, ">")
	return strings.TrimSpace(token)
}
