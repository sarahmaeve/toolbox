package pdfocr

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

// RenderMarkdownPages renders related OCR pages with document-wide citation
// context. Scholarly footnotes are normally numbered monotonically, so clear
// numeric definitions act as anchors for damaged one-glyph readings on nearby
// definitions and raised references. No page number or document vocabulary is
// embedded in this repair.
func RenderMarkdownPages(pages []Page) []string {
	working := cloneOCRPages(pages)
	for pageIndex := range working {
		repairEnglishOrdinalSuffixes(&working[pageIndex].OCR)
	}
	definitions := repairSequentialFootnoteMarkers(working)
	repairBodyCitationMarkers(working, definitions)

	markdown := make([]string, 0, len(working))
	continuedFromPrevious := false
	for pageIndex, page := range working {
		markdown = append(markdown, renderMarkdown(page.Number, page.OCR, pageIndex == 0, continuedFromPrevious))
		continuedFromPrevious = ocrBodyContinuesOnNextPage(page.OCR)
	}
	return markdown
}

type definitionMarker struct {
	pageIndex   int
	sourceIndex int
	start       int
	end         int
	value       int
}

func repairSequentialFootnoteMarkers(pages []Page) [][]int {
	pageDefinitions := make([][]int, len(pages))
	var candidates []definitionMarker
	for pageIndex := range pages {
		lines, _, _, _ := prepareMarkdownLines(pages[pageIndex].OCR)
		for _, line := range lines {
			if !line.footnote {
				continue
			}
			start, end, value, ok := footnoteMarkerPrefix(line.observation.Text)
			if !ok {
				continue
			}
			candidates = append(candidates, definitionMarker{
				pageIndex: pageIndex, sourceIndex: line.sourceIndex,
				start: start, end: end, value: value,
			})
		}
	}

	sequence := inferFootnoteSequence(candidates)
	if len(sequence) != len(candidates) {
		for _, candidate := range candidates {
			if candidate.value > 0 {
				pageDefinitions[candidate.pageIndex] = append(pageDefinitions[candidate.pageIndex], candidate.value)
			}
		}
		return pageDefinitions
	}

	for index, candidate := range candidates {
		expected := sequence[index]
		observation := &pages[candidate.pageIndex].OCR.Observations[candidate.sourceIndex]
		replaceObservationRange(observation, candidate.start, candidate.end, strconv.Itoa(expected), false)
		pageDefinitions[candidate.pageIndex] = append(pageDefinitions[candidate.pageIndex], expected)
	}
	return pageDefinitions
}

// footnoteMarkerPrefix recognizes a compact leading note token. Alongside
// digits it admits short, digit-like OCR mixtures (for example "3A", "s0", or
// "5'"); document-wide numeric anchors decide their eventual value. Requiring
// a compact token boundary avoids treating an ordinary leading word as a note.
func footnoteMarkerPrefix(text string) (start, end, value int, ok bool) {
	runes := []rune(text)
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	if start >= len(runes) {
		return 0, 0, 0, false
	}
	end = start
	for end < len(runes) && !unicode.IsSpace(runes[end]) && end-start < 3 {
		end++
	}
	if end == start || end < len(runes) && !unicode.IsSpace(runes[end]) {
		return 0, 0, 0, false
	}
	token := runes[start:end]
	allDigits := true
	for _, character := range token {
		allDigits = allDigits && unicode.IsDigit(character)
	}
	if allDigits && (end < len(runes) || len(token) <= 2) {
		parsed, err := strconv.Atoi(string(token))
		return start, end, parsed, err == nil
	}
	if len(token) == 1 && (unicode.IsPunct(token[0]) || unicode.IsSymbol(token[0])) {
		return start, end, 0, true
	}
	if len(token) <= 2 {
		hasDigit := false
		for _, character := range token {
			hasDigit = hasDigit || unicode.IsDigit(character)
			if !strings.ContainsRune("0123456789lIioOsSeEtTA°'\"", character) {
				return 0, 0, 0, false
			}
		}
		if hasDigit {
			return start, end, 0, true
		}
	}
	return 0, 0, 0, false
}

func inferFootnoteSequence(candidates []definitionMarker) []int {
	if len(candidates) == 0 {
		return nil
	}
	maxObserved := 0
	numericAnchors := 0
	for _, candidate := range candidates {
		if candidate.value > 0 {
			numericAnchors++
			maxObserved = max(maxObserved, candidate.value)
		}
	}
	if numericAnchors == 0 {
		return nil
	}
	maxValue := maxObserved + len(candidates) + 5
	const unreachable = int(^uint(0)>>1) / 4
	cost := make([][]int, len(candidates))
	previous := make([][]int, len(candidates))
	for index := range candidates {
		cost[index] = make([]int, maxValue+1)
		previous[index] = make([]int, maxValue+1)
		for value := range cost[index] {
			cost[index][value] = unreachable
			previous[index][value] = -1
		}
	}
	for value := 1; value <= maxValue; value++ {
		cost[0][value] = footnoteObservationCost(candidates[0], value)
	}
	for index := 1; index < len(candidates); index++ {
		for value := 2; value <= maxValue; value++ {
			observationCost := footnoteObservationCost(candidates[index], value)
			for prior := 1; prior < value; prior++ {
				candidateCost := cost[index-1][prior]
				if candidateCost == unreachable {
					continue
				}
				candidateCost += 3*(value-prior-1) + observationCost
				if candidateCost < cost[index][value] {
					cost[index][value] = candidateCost
					previous[index][value] = prior
				}
			}
		}
	}

	lastValue, bestCost := 0, unreachable
	lastIndex := len(candidates) - 1
	for value := 1; value <= maxValue; value++ {
		if cost[lastIndex][value] < bestCost {
			lastValue, bestCost = value, cost[lastIndex][value]
		}
	}
	if lastValue == 0 || bestCost == unreachable {
		return nil
	}
	sequence := make([]int, len(candidates))
	for index := lastIndex; index >= 0; index-- {
		sequence[index] = lastValue
		lastValue = previous[index][lastValue]
	}
	return sequence
}

func footnoteObservationCost(candidate definitionMarker, expected int) int {
	if candidate.value == 0 || candidate.value == expected {
		return 0
	}
	return 20 + min(10, absoluteInt(candidate.value-expected))
}

type citationCandidate struct {
	sourceIndex int
	start       int
	end         int
	value       int
	score       int
}

type citationMatch struct {
	candidate citationCandidate
	value     int
}

func repairBodyCitationMarkers(pages []Page, definitions [][]int) {
	for pageIndex := range pages {
		if len(definitions[pageIndex]) == 0 {
			continue
		}
		lines, _, _, _ := prepareMarkdownLines(pages[pageIndex].OCR)
		candidates := bodyCitationCandidates(lines)
		matches := alignCitationCandidates(candidates, definitions[pageIndex])
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].candidate.sourceIndex != matches[j].candidate.sourceIndex {
				return matches[i].candidate.sourceIndex > matches[j].candidate.sourceIndex
			}
			return matches[i].candidate.start > matches[j].candidate.start
		})
		for _, match := range matches {
			observation := &pages[pageIndex].OCR.Observations[match.candidate.sourceIndex]
			replaceObservationRange(
				observation,
				match.candidate.start,
				match.candidate.end,
				strconv.Itoa(match.value),
				true,
			)
		}
	}
}

func bodyCitationCandidates(lines []ocrMarkdownLine) []citationCandidate {
	var candidates []citationCandidate
	for _, line := range lines {
		if line.running || line.footnote {
			continue
		}
		observation := line.observation
		runes := []rune(observation.Text)
		superscript := observationSuperscriptRunes(observation)
		for index := 0; index < len(runes); {
			if end, value, ok := attachedCitationToken(runes, index); ok {
				score := 12
				for position := index; position < end; position++ {
					if superscript[position] {
						score += 8
						break
					}
				}
				candidates = append(candidates, citationCandidate{
					sourceIndex: line.sourceIndex, start: index, end: end,
					value: value, score: score,
				})
				index = end
				continue
			}
			if unicode.IsDigit(runes[index]) && superscript[index] {
				end := index + 1
				for end < len(runes) && unicode.IsDigit(runes[end]) && superscript[end] {
					end++
				}
				value, _ := strconv.Atoi(string(runes[index:end]))
				candidates = append(candidates, citationCandidate{
					sourceIndex: line.sourceIndex, start: index, end: end,
					value: value, score: 24,
				})
				index = end
				continue
			}

			character := runes[index]
			if !strings.ContainsRune("'\"&*§$†‡?", character) {
				index++
				continue
			}
			previousIndex := previousNonSpaceIndex(runes, index)
			previous, hasPrevious := rune(0), previousIndex >= 0
			if hasPrevious {
				previous = runes[previousIndex]
			}
			next, hasNext := nextNonSpaceRune(runes, index)
			ordinalCluster := hasPrevious && unicode.IsDigit(previous)
			if hasPrevious && strings.ContainsRune("*'\"", previous) {
				beforePrevious := previousNonSpaceIndex(runes, previousIndex)
				ordinalCluster = beforePrevious >= 0 && unicode.IsDigit(runes[beforePrevious])
			}
			if ordinalCluster {
				// OCR commonly renders a raised ordinal suffix as '*' or a
				// quote. It is not a footnote reference.
				index++
				continue
			}
			if character == '\'' && hasPrevious && hasNext && unicode.IsLetter(previous) && unicode.IsLetter(next) {
				index++
				continue
			}
			afterPunctuation := hasPrevious && strings.ContainsRune(".,;:!?)]}”’", previous)
			atEnd := !hasNext
			center := line.x + line.width/2
			titleEndMarker := atEnd && line.top >= 0.78 && center >= 0.36 && center <= 0.64
			if character == '?' && !afterPunctuation && !superscript[index] {
				index++
				continue
			}
			if !afterPunctuation && !titleEndMarker && !superscript[index] {
				index++
				continue
			}
			candidateEnd := index + 1
			for candidateEnd < len(runes) && candidateEnd-index < 3 &&
				strings.ContainsRune("0123456789lIioOsSeEtT°'\"", runes[candidateEnd]) {
				candidateEnd++
			}
			if candidateEnd < len(runes) && !unicode.IsSpace(runes[candidateEnd]) &&
				!strings.ContainsRune(",.;:!?)]}", runes[candidateEnd]) {
				candidateEnd = index + 1
			}
			score := 7
			if afterPunctuation {
				score += 5
			}
			if atEnd {
				score += 3
			}
			if superscript[index] {
				score += 8
			}
			candidates = append(candidates, citationCandidate{
				sourceIndex: line.sourceIndex, start: index, end: candidateEnd, score: score,
			})
			index = candidateEnd
		}
	}
	return candidates
}

func attachedCitationToken(runes []rune, start int) (end, value int, ok bool) {
	if start <= 0 || start >= len(runes) ||
		!strings.ContainsRune("0123456789lIioOsSeEtT°", runes[start]) ||
		!strings.ContainsRune(".,;:!?)]}”’", runes[start-1]) {
		return 0, 0, false
	}
	end = start
	hasDigit := false
	for end < len(runes) && end-start < 3 && strings.ContainsRune("0123456789lIioOsSeEtT°'\"", runes[end]) {
		hasDigit = hasDigit || unicode.IsDigit(runes[end])
		end++
	}
	if end == start || end < len(runes) && !unicode.IsSpace(runes[end]) && !strings.ContainsRune(",.;:!?)]}", runes[end]) {
		return 0, 0, false
	}
	if !hasDigit && end-start != 1 {
		return 0, 0, false
	}
	if hasDigit {
		allDigits := true
		for _, character := range runes[start:end] {
			allDigits = allDigits && unicode.IsDigit(character)
		}
		if allDigits {
			value, _ = strconv.Atoi(string(runes[start:end]))
		}
	}
	return end, value, true
}

func ocrBodyContinuesOnNextPage(result ocr.Result) bool {
	lines, _, _, _ := prepareMarkdownLines(result)
	for index := len(lines) - 1; index >= 0; index-- {
		if lines[index].running || lines[index].footnote {
			continue
		}
		return !endsOCRParagraph(lines[index].plain)
	}
	return false
}

func observationSuperscriptRunes(observation ocr.Observation) []bool {
	copyOf := observation
	detectObservationSuperscripts(&copyOf)
	runes := []rune(copyOf.Text)
	result := make([]bool, len(runes))
	for _, span := range copyOf.Styles {
		if !span.Superscript {
			continue
		}
		for index := max(0, span.Start); index < min(len(result), span.End); index++ {
			result[index] = true
		}
	}
	for _, symbol := range copyOf.Symbols {
		if symbol.Superscript && symbol.RuneIndex >= 0 && symbol.RuneIndex < len(result) {
			result[symbol.RuneIndex] = true
		}
	}
	return result
}

func previousNonSpaceRune(runes []rune, index int) (rune, bool) {
	index = previousNonSpaceIndex(runes, index)
	if index >= 0 {
		return runes[index], true
	}
	return 0, false
}

func previousNonSpaceIndex(runes []rune, index int) int {
	for index--; index >= 0; index-- {
		if !unicode.IsSpace(runes[index]) {
			return index
		}
	}
	return -1
}

func nextNonSpaceRune(runes []rune, index int) (rune, bool) {
	for index++; index < len(runes); index++ {
		if !unicode.IsSpace(runes[index]) {
			return runes[index], true
		}
	}
	return 0, false
}

type citationAlignment struct {
	score   int
	matches []citationMatch
}

func alignCitationCandidates(candidates []citationCandidate, expected []int) []citationMatch {
	type state struct{ candidate, expected int }
	memo := make(map[state]citationAlignment)
	seen := make(map[state]bool)
	var solve func(int, int) citationAlignment
	solve = func(candidateIndex, expectedIndex int) citationAlignment {
		key := state{candidateIndex, expectedIndex}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		if candidateIndex >= len(candidates) || expectedIndex >= len(expected) {
			return citationAlignment{}
		}

		best := solve(candidateIndex+1, expectedIndex)
		if skippedExpected := solve(candidateIndex, expectedIndex+1); betterCitationAlignment(skippedExpected, best) {
			best = skippedExpected
		}

		candidate := candidates[candidateIndex]
		value := expected[expectedIndex]
		matchScore := candidate.score - 2*absoluteInt(candidateIndex-expectedIndex)
		if candidate.value > 0 {
			if candidate.value == value {
				matchScore += 30
			} else {
				matchScore -= 6
			}
		}
		matchedTail := solve(candidateIndex+1, expectedIndex+1)
		matched := citationAlignment{
			score: matchScore + matchedTail.score,
			matches: append([]citationMatch{{candidate: candidate, value: value}},
				matchedTail.matches...),
		}
		if betterCitationAlignment(matched, best) {
			best = matched
		}
		memo[key] = best
		return best
	}
	return solve(0, 0).matches
}

func betterCitationAlignment(candidate, current citationAlignment) bool {
	if candidate.score != current.score {
		return candidate.score > current.score
	}
	if len(candidate.matches) != len(current.matches) {
		return len(candidate.matches) > len(current.matches)
	}
	for index := range candidate.matches {
		if candidate.matches[index].value != current.matches[index].value {
			return candidate.matches[index].value < current.matches[index].value
		}
	}
	return false
}

func absoluteInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func replaceObservationRange(observation *ocr.Observation, start, end int, replacement string, superscript bool) {
	if observation == nil {
		return
	}
	runes := []rune(observation.Text)
	if start < 0 || end < start || end > len(runes) {
		return
	}
	replacementRunes := []rune(replacement)
	delta := len(replacementRunes) - (end - start)
	observation.Text = string(runes[:start]) + replacement + string(runes[end:])

	adjustedStyles := make([]ocr.StyleSpan, 0, len(observation.Styles)+1)
	for _, span := range observation.Styles {
		switch {
		case span.End <= start:
			adjustedStyles = append(adjustedStyles, span)
		case span.Start >= end:
			span.Start += delta
			span.End += delta
			adjustedStyles = append(adjustedStyles, span)
		default:
			span.Start = min(span.Start, start)
			span.End = max(start+len(replacementRunes), span.End+delta)
			adjustedStyles = append(adjustedStyles, span)
		}
	}
	if superscript {
		adjustedStyles = append(adjustedStyles, ocr.StyleSpan{
			Start: start, End: start + len(replacementRunes), Superscript: true,
		})
	}
	observation.Styles = adjustedStyles

	adjustedSymbols := make([]ocr.Symbol, 0, len(observation.Symbols))
	replacedSymbol := false
	for _, symbol := range observation.Symbols {
		switch {
		case symbol.RuneIndex < start:
			adjustedSymbols = append(adjustedSymbols, symbol)
		case symbol.RuneIndex >= end:
			symbol.RuneIndex += delta
			adjustedSymbols = append(adjustedSymbols, symbol)
		case !replacedSymbol:
			symbol.RuneIndex = start
			symbol.Text = replacement
			symbol.Superscript = superscript
			adjustedSymbols = append(adjustedSymbols, symbol)
			replacedSymbol = true
		}
	}
	observation.Symbols = adjustedSymbols
}

func sliceObservation(observation ocr.Observation, start, end int) ocr.Observation {
	runes := []rune(observation.Text)
	start, end = max(0, start), min(len(runes), end)
	if end < start {
		end = start
	}
	observation.Text = string(runes[start:end])
	observation.Candidates = nil

	styles := observation.Styles[:0]
	for _, span := range observation.Styles {
		spanStart := max(start, span.Start)
		spanEnd := min(end, span.End)
		if spanStart < spanEnd {
			span.Start = spanStart - start
			span.End = spanEnd - start
			styles = append(styles, span)
		}
	}
	observation.Styles = styles

	symbols := observation.Symbols[:0]
	for _, symbol := range observation.Symbols {
		if symbol.RuneIndex >= start && symbol.RuneIndex < end {
			symbol.RuneIndex -= start
			symbols = append(symbols, symbol)
		}
	}
	observation.Symbols = symbols
	return observation
}

func cloneOCRPages(pages []Page) []Page {
	cloned := make([]Page, len(pages))
	for pageIndex, page := range pages {
		cloned[pageIndex] = page
		cloned[pageIndex].OCR.Observations = make([]ocr.Observation, len(page.OCR.Observations))
		for observationIndex, observation := range page.OCR.Observations {
			clonedObservation := observation
			clonedObservation.Candidates = append([]ocr.Candidate(nil), observation.Candidates...)
			clonedObservation.Symbols = append([]ocr.Symbol(nil), observation.Symbols...)
			clonedObservation.Styles = append([]ocr.StyleSpan(nil), observation.Styles...)
			cloned[pageIndex].OCR.Observations[observationIndex] = clonedObservation
		}
	}
	return cloned
}
