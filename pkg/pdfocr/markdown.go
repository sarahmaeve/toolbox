package pdfocr

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

type inlineFlags uint8

const (
	flagItalic inlineFlags = 1 << iota
	flagBold
	flagSuperscript
	flagSubscript
)

type inlineRun struct {
	text  string
	flags inlineFlags
}

type ocrMarkdownLine struct {
	observation ocr.Observation
	sourceIndex int
	plain       string
	runs        []inlineRun
	x, y, top   float64
	width       float64
	height      float64
	running     bool
	footnote    bool
	heading     bool
}

// RenderMarkdown converts one geometry- and style-enriched OCR page to
// Markdown. It removes running heads, reconstructs paragraphs, retains raster-
// detected italics and superscripts, and separates the smaller footnote zone.
func RenderMarkdown(pageNumber int, result ocr.Result) string {
	return renderMarkdown(pageNumber, result, true, false)
}

func renderMarkdown(pageNumber int, result ocr.Result, allowTitle, continuedFromPrevious bool) string {
	lines, bodyHeight, normalGap, bodyMargin := prepareMarkdownLines(result)
	if len(lines) == 0 {
		return fmt.Sprintf("<!-- PDF page %d -->", pageNumber)
	}

	if continuedFromPrevious {
		for index := range lines {
			if !lines[index].running && !lines[index].footnote {
				lines[index].heading = false
				break
			}
		}
	}
	titleStart, titleEnd, metadataEnd := -1, -1, -1
	if allowTitle && !continuedFromPrevious {
		titleStart, titleEnd, metadataEnd = findTitleBlock(lines, bodyHeight, bodyMargin)
	}
	var blocks []string
	if titleStart >= 0 {
		var titleRuns []inlineRun
		for i := titleStart; i < titleEnd; i++ {
			titleRuns = appendInlineSpace(titleRuns)
			titleRuns = appendInlineRuns(titleRuns, lines[i].runs)
		}
		blocks = append(blocks, "# "+renderInlineRuns(titleRuns))
		if titleEnd < metadataEnd {
			var metadata []string
			for i := titleEnd; i < metadataEnd; i++ {
				metadata = append(metadata, renderInlineRuns(lines[i].runs))
			}
			blocks = append(blocks, strings.Join(metadata, "  \n"))
		}
	}

	var paragraph []inlineRun
	flushParagraph := func() {
		if text := strings.TrimSpace(renderInlineRuns(paragraph)); text != "" {
			blocks = append(blocks, text)
		}
		paragraph = nil
	}
	previousBody := -1
	for i := range lines {
		line := &lines[i]
		if line.running || line.footnote || i >= titleStart && i < metadataEnd {
			continue
		}
		if line.heading {
			flushParagraph()
			blocks = append(blocks, "## "+renderInlineRuns(line.runs))
			previousBody = -1
			continue
		}

		newParagraph := false
		if len(paragraph) > 0 && previousBody >= 0 {
			previous := lines[previousBody]
			if line.x > bodyMargin+0.018 && endsOCRParagraph(previous.plain) {
				newParagraph = true
			}
			if previous.y-line.y > normalGap*1.40 {
				newParagraph = true
			}
		}
		if newParagraph {
			flushParagraph()
		}
		paragraph = joinOCRLine(paragraph, line.runs, line.plain)
		previousBody = i
	}
	flushParagraph()

	if footnotes := renderOCRFootnotes(lines); footnotes != "" {
		blocks = append(blocks, "---\n\n"+footnotes)
	}

	var output strings.Builder
	fmt.Fprintf(&output, "<!-- PDF page %d -->", pageNumber)
	if len(blocks) > 0 {
		output.WriteString("\n\n")
		output.WriteString(strings.Join(blocks, "\n\n"))
	}
	return strings.TrimSpace(output.String())
}

func markdownLines(result ocr.Result) []ocrMarkdownLine {
	lines := make([]ocrMarkdownLine, 0, len(result.Observations))
	for sourceIndex, source := range result.Observations {
		plain := strings.TrimSpace(source.Text)
		if plain == "" || !validNormalizedBounds(source.Bounds) {
			continue
		}
		detectObservationSuperscripts(&source)
		lines = append(lines, ocrMarkdownLine{
			observation: source,
			sourceIndex: sourceIndex,
			plain:       plain,
			runs:        observationInlineRuns(source),
			x:           source.Bounds.X,
			y:           source.Bounds.Y,
			top:         source.Bounds.Y + source.Bounds.Height,
			width:       source.Bounds.Width,
			height:      source.Bounds.Height,
		})
	}
	return lines
}

func prepareMarkdownLines(result ocr.Result) ([]ocrMarkdownLine, float64, float64, float64) {
	lines := markdownLines(result)
	if len(lines) == 0 {
		return nil, 0, 0, 0
	}
	bodyHeight := dominantOCRLineHeight(lines)
	markRunningHeaders(lines)
	normalGap := dominantOCRLineGap(lines, bodyHeight)
	bodyMargin := ocrBodyMargin(lines, bodyHeight)
	if footnoteStart := findFootnoteStart(lines, bodyHeight, normalGap); footnoteStart >= 0 {
		for i := footnoteStart; i < len(lines); i++ {
			if !lines[i].running {
				lines[i].footnote = true
			}
		}
	}
	for i := range lines {
		lines[i].heading = likelyOCRHeading(lines, i, bodyHeight, bodyMargin, normalGap)
	}
	return lines, bodyHeight, normalGap, bodyMargin
}

func detectObservationSuperscripts(observation *ocr.Observation) {
	words := observationWords(*observation)
	if len(words) < 2 {
		return
	}
	var baselineY, baselineHeight []float64
	for _, word := range words {
		if isShortNumber(word.Text) {
			continue
		}
		baselineY = append(baselineY, word.Bounds.Y)
		baselineHeight = append(baselineHeight, word.Bounds.Height)
	}
	if len(baselineHeight) == 0 {
		return
	}
	medianY := medianFloat64(baselineY)
	medianHeight := medianFloat64(baselineHeight)
	for _, word := range words {
		if !isShortNumber(word.Text) {
			continue
		}
		if word.Bounds.Height < medianHeight*0.86 || word.Bounds.Y > medianY+medianHeight*0.18 {
			observation.Styles = append(observation.Styles, ocr.StyleSpan{
				Start: word.Start, End: word.End, Superscript: true,
			})
		}
	}
}

func isShortNumber(text string) bool {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 || len(runes) > 3 {
		return false
	}
	for _, character := range runes {
		if !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}

func observationInlineRuns(observation ocr.Observation) []inlineRun {
	runes := []rune(observation.Text)
	flags := make([]inlineFlags, len(runes))
	for _, span := range observation.Styles {
		start, end := max(0, span.Start), min(len(runes), span.End)
		var value inlineFlags
		if span.Italic {
			value |= flagItalic
		}
		if span.Bold {
			value |= flagBold
		}
		if span.Superscript {
			value |= flagSuperscript
		}
		if span.Subscript {
			value |= flagSubscript
		}
		for i := start; i < end; i++ {
			flags[i] |= value
		}
	}
	for _, symbol := range observation.Symbols {
		if symbol.RuneIndex >= 0 && symbol.RuneIndex < len(flags) && symbol.Superscript {
			flags[symbol.RuneIndex] |= flagSuperscript
		}
	}

	// Include whitespace and light punctuation between matching italic words
	// in the same emphasis span.
	for i := 1; i+1 < len(runes); i++ {
		if flags[i]&flagItalic != 0 || !unicode.IsSpace(runes[i]) && !unicode.IsPunct(runes[i]) {
			continue
		}
		left, right := i-1, i+1
		for left >= 0 && (unicode.IsSpace(runes[left]) || unicode.IsPunct(runes[left])) {
			left--
		}
		for right < len(runes) && (unicode.IsSpace(runes[right]) || unicode.IsPunct(runes[right])) {
			right++
		}
		if left >= 0 && right < len(runes) && flags[left]&flagItalic != 0 && flags[right]&flagItalic != 0 {
			flags[i] |= flagItalic
		}
	}

	var runs []inlineRun
	for i, character := range runes {
		if len(runs) == 0 || runs[len(runs)-1].flags != flags[i] {
			runs = append(runs, inlineRun{flags: flags[i]})
		}
		runs[len(runs)-1].text += string(character)
	}
	return trimInlineRuns(runs)
}

func trimInlineRuns(runs []inlineRun) []inlineRun {
	for len(runs) > 0 {
		trimmed := strings.TrimLeftFunc(runs[0].text, unicode.IsSpace)
		if trimmed != "" {
			runs[0].text = trimmed
			break
		}
		runs = runs[1:]
	}
	for len(runs) > 0 {
		last := len(runs) - 1
		trimmed := strings.TrimRightFunc(runs[last].text, unicode.IsSpace)
		if trimmed != "" {
			runs[last].text = trimmed
			break
		}
		runs = runs[:last]
	}
	return runs
}

func renderInlineRuns(runs []inlineRun) string {
	var output strings.Builder
	for _, run := range mergeAdjacentInlineRuns(runs) {
		text := escapeOCRMarkdown(run.text)
		switch {
		case run.flags&flagSuperscript != 0:
			output.WriteString("<sup>" + text + "</sup>")
		case run.flags&flagSubscript != 0:
			output.WriteString("<sub>" + text + "</sub>")
		case run.flags&flagBold != 0 && run.flags&flagItalic != 0:
			output.WriteString("***" + text + "***")
		case run.flags&flagBold != 0:
			output.WriteString("**" + text + "**")
		case run.flags&flagItalic != 0:
			output.WriteString("*" + text + "*")
		default:
			output.WriteString(text)
		}
	}
	return strings.TrimSpace(output.String())
}

func mergeAdjacentInlineRuns(runs []inlineRun) []inlineRun {
	merged := make([]inlineRun, 0, len(runs))
	for _, run := range runs {
		if run.text == "" {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].flags == run.flags {
			merged[len(merged)-1].text += run.text
		} else {
			merged = append(merged, run)
		}
	}
	return merged
}

func joinOCRLine(paragraph, line []inlineRun, plain string) []inlineRun {
	if len(paragraph) == 0 {
		return appendInlineRuns(nil, line)
	}
	first, _ := utf8.DecodeRuneInString(strings.TrimSpace(plain))
	if inlineEndsWithRune(paragraph, '-') && unicode.IsLower(first) {
		wordStyle := trailingWordStyle(paragraph)
		removeTrailingInlineRune(&paragraph, '-')
		line = applyLeadingWordStyle(line, wordStyle)
		return appendInlineRuns(paragraph, line)
	}
	paragraph = appendInlineSpace(paragraph)
	return appendInlineRuns(paragraph, line)
}

func trailingWordStyle(runs []inlineRun) inlineFlags {
	for index := len(runs) - 1; index >= 0; index-- {
		for _, character := range []rune(strings.TrimRightFunc(runs[index].text, unicode.IsSpace)) {
			if unicode.IsLetter(character) || unicode.IsMark(character) || character == '-' {
				return runs[index].flags & (flagItalic | flagBold)
			}
		}
	}
	return 0
}

func applyLeadingWordStyle(runs []inlineRun, style inlineFlags) []inlineRun {
	style &= flagItalic | flagBold
	if style == 0 {
		return runs
	}
	var styled []inlineRun
	inWord := true
	for _, run := range runs {
		var current inlineRun
		flush := func() {
			if current.text != "" {
				styled = appendInlineRuns(styled, []inlineRun{current})
				current = inlineRun{}
			}
		}
		for _, character := range run.text {
			flags := run.flags
			if inWord && (unicode.IsLetter(character) || unicode.IsMark(character)) {
				flags |= style
			} else {
				inWord = false
			}
			if current.text != "" && current.flags != flags {
				flush()
			}
			current.flags = flags
			current.text += string(character)
		}
		flush()
	}
	return styled
}

func inlineEndsWithRune(runs []inlineRun, target rune) bool {
	for i := len(runs) - 1; i >= 0; i-- {
		trimmed := strings.TrimRightFunc(runs[i].text, unicode.IsSpace)
		if trimmed == "" {
			continue
		}
		last, _ := utf8.DecodeLastRuneInString(trimmed)
		return last == target
	}
	return false
}

func removeTrailingInlineRune(runs *[]inlineRun, target rune) bool {
	if runs == nil {
		return false
	}
	for i := len(*runs) - 1; i >= 0; i-- {
		text := (*runs)[i].text
		trimmed := strings.TrimRightFunc(text, unicode.IsSpace)
		last, size := utf8.DecodeLastRuneInString(trimmed)
		if last != target {
			return false
		}
		(*runs)[i].text = trimmed[:len(trimmed)-size]
		*runs = trimInlineRuns(*runs)
		return true
	}
	return false
}

func appendInlineSpace(runs []inlineRun) []inlineRun {
	if len(runs) == 0 {
		return runs
	}
	return appendInlineRuns(runs, []inlineRun{{text: " "}})
}

func appendInlineRuns(destination, source []inlineRun) []inlineRun {
	for _, run := range source {
		if run.text == "" {
			continue
		}
		if len(destination) > 0 && destination[len(destination)-1].flags == run.flags {
			destination[len(destination)-1].text += run.text
		} else {
			destination = append(destination, run)
		}
	}
	return destination
}

func inlinePlainText(runs []inlineRun) string {
	var output strings.Builder
	for _, run := range runs {
		output.WriteString(run.text)
	}
	return output.String()
}

func dominantOCRLineHeight(lines []ocrMarkdownLine) float64 {
	var heights []float64
	for _, line := range lines {
		if line.y < 0.25 || line.y > 0.90 || utf8.RuneCountInString(line.plain) < 12 {
			continue
		}
		weight := min(8, max(1, utf8.RuneCountInString(line.plain)/16))
		for range weight {
			heights = append(heights, line.height)
		}
	}
	if len(heights) == 0 {
		return 0.017
	}
	sort.Float64s(heights)
	return heights[int(float64(len(heights)-1)*0.65)]
}

func markRunningHeaders(lines []ocrMarkdownLine) {
	for i := range lines {
		if lines[i].top > 0.915 {
			lines[i].running = true
			continue
		}
		if lines[i].top > 0.86 && isShortNumber(lines[i].plain) {
			lines[i].running = true
		}
	}
}

func dominantOCRLineGap(lines []ocrMarkdownLine, bodyHeight float64) float64 {
	var gaps []float64
	previous := -1
	for i, line := range lines {
		if line.running || line.height < bodyHeight*0.80 || line.height > bodyHeight*1.25 {
			continue
		}
		if previous >= 0 {
			gap := lines[previous].y - line.y
			if gap > bodyHeight*0.55 && gap < bodyHeight*2.2 {
				gaps = append(gaps, gap)
			}
		}
		previous = i
	}
	if len(gaps) == 0 {
		return bodyHeight * 1.05
	}
	return medianFloat64(gaps)
}

func ocrBodyMargin(lines []ocrMarkdownLine, bodyHeight float64) float64 {
	var positions []float64
	for _, line := range lines {
		if line.running || line.height < bodyHeight*0.82 || line.height > bodyHeight*1.20 || utf8.RuneCountInString(line.plain) < 10 {
			continue
		}
		positions = append(positions, line.x)
	}
	if len(positions) == 0 {
		return 0
	}
	sort.Float64s(positions)
	return positions[min(len(positions)-1, len(positions)/10)]
}

func findFootnoteStart(lines []ocrMarkdownLine, bodyHeight, normalGap float64) int {
	previous := -1
	for i, line := range lines {
		if line.running {
			continue
		}
		if line.y > 0.50 {
			previous = i
			continue
		}
		compactFollowing := 0
		markerFollowing := false
		seen := 0
		followingPrevious := -1
		for j := i; j < len(lines) && seen < 6; j++ {
			if lines[j].running {
				continue
			}
			if followingPrevious >= 0 && lines[followingPrevious].y-lines[j].y <= normalGap*0.92 {
				compactFollowing++
			}
			followingPrevious = j
			markerFollowing = markerFollowing || startsFootnoteLike(lines[j].plain)
			seen++
		}
		gap := 0.0
		if previous >= 0 {
			gap = lines[previous].y - line.y
		}
		marker := startsFootnoteLike(line.plain)
		separated := gap > normalGap*1.18
		stronglySeparated := gap > normalGap*1.55
		compact := compactFollowing >= min(2, max(0, seen-1))
		if marker && (separated || line.height < bodyHeight*0.90 && compact) {
			return i
		}
		if line.y < 0.36 && markerFollowing && stronglySeparated && (line.height < bodyHeight*1.02 || compact) {
			return i
		}
		previous = i
	}
	return -1
}

func startsFootnoteLike(text string) bool {
	_, _, _, ok := footnoteMarkerPrefix(text)
	return ok
}

func likelyOCRHeading(lines []ocrMarkdownLine, index int, bodyHeight, bodyMargin, normalGap float64) bool {
	line := lines[index]
	if line.running || line.footnote || line.x > bodyMargin+0.018 || utf8.RuneCountInString(line.plain) > 80 {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(line.plain)
	if strings.ContainsRune(".,;:?!-", last) {
		return false
	}
	firstLetter := rune(0)
	for _, character := range line.plain {
		if unicode.IsLetter(character) {
			firstLetter = character
			break
		}
	}
	if firstLetter == 0 || !unicode.IsUpper(firstLetter) {
		return false
	}
	large := line.height > bodyHeight*1.20
	previousGap, nextGap := 0.0, 0.0
	previousFound := false
	nextIndented := false
	for i := index - 1; i >= 0; i-- {
		if !lines[i].running && !lines[i].footnote {
			previousGap = lines[i].y - line.y
			previousFound = true
			break
		}
	}
	for i := index + 1; i < len(lines); i++ {
		if !lines[i].running && !lines[i].footnote {
			nextGap = line.y - lines[i].y
			nextIndented = lines[i].x > bodyMargin+0.018
			break
		}
	}
	lineLength := utf8.RuneCountInString(line.plain)
	if large && (lineLength <= 40 || nextIndented || nextGap > normalGap*1.45) {
		return true
	}
	if !previousFound && lineLength <= 60 && nextGap > normalGap*1.10 && nextIndented {
		return true
	}
	return previousGap > normalGap*1.30 && nextGap > normalGap*1.20 && nextIndented
}

func findTitleBlock(lines []ocrMarkdownLine, bodyHeight, bodyMargin float64) (start, titleEnd, metadataEnd int) {
	start = -1
	for i, line := range lines {
		if line.running || line.footnote {
			continue
		}
		start = i
		break
	}
	if start < 0 || lines[start].top < 0.80 || lines[start].x < bodyMargin+0.035 ||
		lines[start].x+lines[start].width/2 < 0.36 || lines[start].x+lines[start].width/2 > 0.64 {
		return -1, -1, -1
	}
	// The article title and author block are centered. The first subsequent
	// line back at the body margin is the opening section heading.
	blockEnd := start
	for blockEnd < len(lines) {
		line := lines[blockEnd]
		if line.running || line.footnote || line.top < 0.68 {
			break
		}
		if blockEnd > start && line.x <= bodyMargin+0.025 {
			break
		}
		blockEnd++
	}
	if blockEnd == start {
		return -1, -1, -1
	}
	titleEnd = start
	for titleEnd < blockEnd && lines[titleEnd].height >= bodyHeight*1.02 {
		titleEnd++
	}
	if titleEnd == start {
		titleEnd++
	}
	metadataEnd = blockEnd
	return start, titleEnd, metadataEnd
}

func endsOCRParagraph(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(text)
	return strings.ContainsRune(".!?\"'”’)]", last) || unicode.IsDigit(last)
}

func renderOCRFootnotes(lines []ocrMarkdownLine) string {
	var blocks []string
	var marker string
	var content []inlineRun
	flush := func() {
		text := strings.TrimSpace(renderInlineRuns(content))
		if text != "" {
			if marker == "" {
				blocks = append(blocks, "> "+text)
			} else {
				blocks = append(blocks, "<sup>"+escapeOCRMarkdown(marker)+"</sup> "+text)
			}
		}
		marker = ""
		content = nil
	}
	for _, line := range lines {
		if !line.footnote || line.running {
			continue
		}
		lineMarker, remainder, found := splitOCRFootnoteMarker(line)
		if found {
			flush()
			marker = lineMarker
			content = remainder
			continue
		}
		content = joinOCRLine(content, line.runs, line.plain)
	}
	flush()
	return strings.Join(blocks, "\n\n")
}

func splitOCRFootnoteMarker(line ocrMarkdownLine) (string, []inlineRun, bool) {
	_, end, marker, found := footnoteMarkerPrefix(line.observation.Text)
	if !found || marker == 0 {
		return "", nil, false
	}
	runes := []rune(line.observation.Text)
	for end < len(runes) && unicode.IsSpace(runes[end]) {
		end++
	}
	observation := sliceObservation(line.observation, end, len(runes))
	return fmt.Sprint(marker), observationInlineRuns(observation), true
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyOf := append([]float64(nil), values...)
	sort.Float64s(copyOf)
	middle := len(copyOf) / 2
	if len(copyOf)%2 == 1 {
		return copyOf[middle]
	}
	return (copyOf[middle-1] + copyOf[middle]) / 2
}

func escapeOCRMarkdown(text string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
	)
	return replacer.Replace(text)
}
