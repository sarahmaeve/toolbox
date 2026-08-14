package ocr

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// RestoreGlossaryDiacritics restores authoritative glossary spellings when an
// OCR token differs only by Unicode diacritics. It does not use fuzzy edit
// distance: the base letters must match exactly, and ambiguous glossary keys
// are left untouched. The return value is the number of replaced tokens.
func RestoreGlossaryDiacritics(result *Result, glossary []string) int {
	if result == nil || len(glossary) == 0 {
		return 0
	}
	index := buildGlossaryIndex(glossary)
	replacements := 0
	for i := range result.Observations {
		text, changed := restoreText(result.Observations[i].Text, index)
		result.Observations[i].Text = text
		replacements += changed
	}
	return replacements
}

type glossaryEntry struct {
	word      string
	ambiguous bool
}

func buildGlossaryIndex(glossary []string) map[string]glossaryEntry {
	index := make(map[string]glossaryEntry, len(glossary))
	for _, word := range glossary {
		word = strings.TrimSpace(word)
		key := foldWord(word)
		if key == "" {
			continue
		}
		if prior, exists := index[key]; exists && prior.word != word {
			prior.ambiguous = true
			index[key] = prior
			continue
		}
		index[key] = glossaryEntry{word: word}
	}
	return index
}

func restoreText(text string, glossary map[string]glossaryEntry) (string, int) {
	var output strings.Builder
	output.Grow(len(text))
	start := -1
	replacements := 0
	flush := func(end int) {
		if start < 0 {
			return
		}
		token := text[start:end]
		entry, found := glossary[foldWord(token)]
		if found && !entry.ambiguous && entry.word != token {
			output.WriteString(matchCase(entry.word, token))
			replacements++
		} else {
			output.WriteString(token)
		}
		start = -1
	}

	for byteIndex, character := range text {
		if unicode.IsLetter(character) || unicode.IsMark(character) {
			if start < 0 {
				start = byteIndex
			}
			continue
		}
		flush(byteIndex)
		output.WriteRune(character)
	}
	flush(len(text))
	return output.String(), replacements
}

func matchCase(replacement, source string) string {
	letters := make([]rune, 0, len([]rune(source)))
	for _, character := range source {
		if unicode.IsLetter(character) {
			letters = append(letters, character)
		}
	}
	if len(letters) == 0 {
		return replacement
	}
	allUpper, allLower := true, true
	for _, character := range letters {
		allUpper = allUpper && unicode.IsUpper(character)
		allLower = allLower && unicode.IsLower(character)
	}
	switch {
	case allUpper:
		return strings.ToUpper(replacement)
	case allLower:
		return strings.ToLower(replacement)
	case unicode.IsUpper(letters[0]):
		runes := []rune(strings.ToLower(replacement))
		for i := range runes {
			if unicode.IsLetter(runes[i]) {
				runes[i] = unicode.ToUpper(runes[i])
				break
			}
		}
		return string(runes)
	default:
		return replacement
	}
}

func foldWord(word string) string {
	var folded strings.Builder
	for _, character := range norm.NFD.String(word) {
		if unicode.IsMark(character) {
			continue
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			folded.WriteRune(unicode.ToLower(character))
		}
	}
	return folded.String()
}
