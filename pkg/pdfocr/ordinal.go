package pdfocr

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

type ordinalRepair struct {
	start, end int
	suffix     string
}

// repairEnglishOrdinalSuffixes normalizes the compact raised glyphs that OCR
// commonly returns as '*', quotes, or punctuation in phrases such as
// "13th century". The word "century" is required, so citation markers and
// ordinary punctuation after numbers are not rewritten.
func repairEnglishOrdinalSuffixes(result *ocr.Result) int {
	if result == nil {
		return 0
	}
	repairs := 0
	for observationIndex := range result.Observations {
		observation := &result.Observations[observationIndex]
		runes := []rune(observation.Text)
		var pending []ordinalRepair
		for index := 0; index < len(runes); {
			if !unicode.IsDigit(runes[index]) || index > 0 && unicode.IsLetter(runes[index-1]) {
				index++
				continue
			}
			numberStart := index
			for index < len(runes) && unicode.IsDigit(runes[index]) {
				index++
			}
			number, err := strconv.Atoi(string(runes[numberStart:index]))
			if err != nil {
				continue
			}
			expected := englishOrdinalSuffix(number)
			suffixStart, suffixEnd := index, index

			if index+2 <= len(runes) && strings.EqualFold(string(runes[index:index+2]), expected) &&
				followedByCentury(runes, index+2) {
				pending = append(pending, ordinalRepair{start: index, end: index + 2, suffix: expected})
				index += 2
				continue
			}

			for suffixEnd < len(runes) && suffixEnd-suffixStart < 2 && isDamagedOrdinalRune(runes[suffixEnd]) {
				suffixEnd++
			}
			if !followedByCentury(runes, suffixEnd) {
				continue
			}
			pending = append(pending, ordinalRepair{start: suffixStart, end: suffixEnd, suffix: expected})
			index = max(index+1, suffixEnd)
		}

		for index := len(pending) - 1; index >= 0; index-- {
			repair := pending[index]
			current := string(runes[repair.start:repair.end])
			if !strings.EqualFold(current, repair.suffix) {
				replaceObservationRange(observation, repair.start, repair.end, repair.suffix, true)
				repairs++
				continue
			}
			observation.Styles = append(observation.Styles, ocr.StyleSpan{
				Start: repair.start, End: repair.end, Superscript: true,
			})
		}
	}
	return repairs
}

func followedByCentury(runes []rune, index int) bool {
	if index < len(runes) && runes[index] == '-' {
		index++
	}
	for index < len(runes) && unicode.IsSpace(runes[index]) {
		index++
	}
	const word = "century"
	if index+len(word) > len(runes) || !strings.EqualFold(string(runes[index:index+len(word)]), word) {
		return false
	}
	end := index + len(word)
	return end == len(runes) || !unicode.IsLetter(runes[end])
}

func isDamagedOrdinalRune(character rune) bool {
	return strings.ContainsRune("*'\"’′.tThHùÙ", character)
}

func englishOrdinalSuffix(number int) string {
	lastTwo := number % 100
	if lastTwo >= 11 && lastTwo <= 13 {
		return "th"
	}
	switch number % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}
