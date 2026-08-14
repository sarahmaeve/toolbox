package pdf

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// positionedTextRun is one text-show operation with the PDF text state that
// was active when it was painted. Keeping coordinates and font metadata lets
// the Markdown path recover structure that the plain-text path intentionally
// discards.
type positionedTextRun struct {
	text       string
	x, y       float64
	fontSize   float64
	fontName   string
	baseFont   string
	italic     bool
	bold       bool
	sourceRank int
}

type positionedTextParser struct {
	data  []byte
	fonts map[string]pageFont

	operands []token
	inText   bool

	fontName string
	fontSize float64
	x, y     float64
	rise     float64
	leading  float64
	hasPoint bool

	runs []positionedTextRun
}

func extractPositionedText(data []byte, fonts map[string]pageFont) []positionedTextRun {
	p := &positionedTextParser{data: data, fonts: fonts}
	p.parse()
	return p.runs
}

func (p *positionedTextParser) parse() {
	for pos := 0; pos < len(p.data); {
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
		if tok.kind != tokOperator {
			p.operands = append(p.operands, tok)
			continue
		}
		p.execute(tok.s)
		p.operands = p.operands[:0]
	}
}

func (p *positionedTextParser) execute(op string) {
	switch op {
	case "BT":
		p.inText = true
		p.hasPoint = false
	case "ET":
		p.inText = false
	case "Tf":
		if len(p.operands) >= 2 {
			name := p.operands[len(p.operands)-2]
			size := p.operands[len(p.operands)-1]
			if name.kind == tokName && size.kind == tokNumber {
				p.fontName = strings.TrimPrefix(name.s, "/")
				p.fontSize = math.Abs(size.n)
			}
		}
	case "Tm":
		if len(p.operands) >= 6 {
			p.x = p.operands[len(p.operands)-2].n
			p.y = p.operands[len(p.operands)-1].n
			p.hasPoint = true
		}
	case "Td", "TD":
		if len(p.operands) >= 2 {
			tx := p.operands[len(p.operands)-2].n
			ty := p.operands[len(p.operands)-1].n
			p.x += tx
			p.y += ty
			p.hasPoint = true
			if op == "TD" {
				p.leading = -ty
			}
		}
	case "TL":
		if len(p.operands) >= 1 {
			p.leading = p.operands[len(p.operands)-1].n
		}
	case "Ts":
		if len(p.operands) >= 1 {
			p.rise = p.operands[len(p.operands)-1].n
		}
	case "T*":
		p.y -= p.leading
		p.hasPoint = true
	case "Tj":
		if len(p.operands) >= 1 {
			p.showString(p.operands[len(p.operands)-1])
		}
	case "TJ":
		if len(p.operands) >= 1 {
			p.showArray(p.operands[len(p.operands)-1])
		}
	case "'":
		p.y -= p.leading
		p.hasPoint = true
		if len(p.operands) >= 1 {
			p.showString(p.operands[len(p.operands)-1])
		}
	case "\"":
		p.y -= p.leading
		p.hasPoint = true
		if len(p.operands) >= 1 {
			p.showString(p.operands[len(p.operands)-1])
		}
	}
}

func (p *positionedTextParser) showString(tok token) {
	if !p.inText || !p.hasPoint || tok.kind != tokString {
		return
	}
	font := p.fonts[p.fontName]
	p.appendRun(decodeTextString(tok.s, font.cmap), font)
}

func (p *positionedTextParser) showArray(tok token) {
	if !p.inText || !p.hasPoint || tok.kind != tokArray {
		return
	}
	font := p.fonts[p.fontName]
	var text strings.Builder
	for _, elem := range tok.arr {
		switch elem.kind {
		case tokString:
			text.WriteString(decodeTextString(elem.s, font.cmap))
		case tokNumber:
			if elem.n < -100 && text.Len() > 0 {
				text.WriteByte(' ')
			}
		}
	}
	p.appendRun(text.String(), font)
}

func (p *positionedTextParser) appendRun(text string, font pageFont) {
	if text == "" {
		return
	}
	p.runs = append(p.runs, positionedTextRun{
		text:       text,
		x:          p.x,
		y:          p.y + p.rise,
		fontSize:   p.fontSize,
		fontName:   p.fontName,
		baseFont:   font.baseName,
		italic:     font.italic,
		bold:       font.bold,
		sourceRank: len(p.runs),
	})
}

type positionedTextLine struct {
	runs              []positionedTextRun
	yWeighted, weight float64
}

func (l *positionedTextLine) anchorY() float64 {
	if l.weight == 0 {
		return 0
	}
	return l.yWeighted / l.weight
}

func (l *positionedTextLine) add(run positionedTextRun) {
	weight := float64(max(1, utf8.RuneCountInString(strings.TrimSpace(run.text))))
	l.runs = append(l.runs, run)
	l.yWeighted += run.y * weight
	l.weight += weight
}

func groupPositionedText(runs []positionedTextRun) []positionedTextLine {
	var lines []positionedTextLine
	for _, run := range runs {
		if strings.TrimSpace(run.text) == "" {
			continue
		}
		if len(lines) == 0 {
			lines = append(lines, positionedTextLine{})
			lines[len(lines)-1].add(run)
			continue
		}

		line := &lines[len(lines)-1]
		tolerance := math.Max(2, math.Max(run.fontSize, lineFontSize(*line))*0.65)
		if math.Abs(run.y-line.anchorY()) > tolerance {
			lines = append(lines, positionedTextLine{})
			line = &lines[len(lines)-1]
		}
		line.add(run)
	}

	for i := range lines {
		sort.SliceStable(lines[i].runs, func(a, b int) bool {
			if math.Abs(lines[i].runs[a].x-lines[i].runs[b].x) < 0.01 {
				return lines[i].runs[a].sourceRank < lines[i].runs[b].sourceRank
			}
			return lines[i].runs[a].x < lines[i].runs[b].x
		})
	}
	return lines
}

func lineFontSize(line positionedTextLine) float64 {
	values := make([]float64, 0, len(line.runs))
	for _, run := range line.runs {
		if run.fontSize > 0 {
			values = append(values, run.fontSize)
		}
	}
	return medianFloat(values)
}

func lineBaseline(line positionedTextLine) float64 {
	var weightedY, weight float64
	for _, run := range line.runs {
		runWeight := float64(max(1, utf8.RuneCountInString(strings.TrimSpace(run.text))))
		weightedY += run.y * runWeight
		weight += runWeight
	}
	if weight == 0 {
		return 0
	}
	return weightedY / weight
}

func lineLeft(line positionedTextLine) float64 {
	if len(line.runs) == 0 {
		return 0
	}
	left := line.runs[0].x
	for _, run := range line.runs[1:] {
		left = math.Min(left, run.x)
	}
	return left
}

func medianFloat(values []float64) float64 {
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

type renderedPositionedLine struct {
	line     positionedTextLine
	text     string
	plain    string
	x, y     float64
	fontSize float64
	header   bool
	footnote bool
	heading  bool
}

func renderPageMarkdown(pageNum int, pageWidth, pageHeight float64, runs []positionedTextRun) string {
	lines := groupPositionedText(runs)
	if len(lines) == 0 {
		return fmt.Sprintf("<!-- PDF page %d -->", pageNum)
	}

	bodySize := dominantFontSize(lines)
	rendered := make([]renderedPositionedLine, 0, len(lines))
	for _, line := range lines {
		y := lineBaseline(line)
		size := lineFontSize(line)
		rendered = append(rendered, renderedPositionedLine{
			line:     line,
			plain:    strings.TrimSpace(joinRawRuns(line.runs)),
			x:        lineLeft(line),
			y:        y,
			fontSize: size,
			header: pageHeight > 0 && (y > pageHeight*0.90 ||
				y > pageHeight*0.85 && size < bodySize*0.85),
			footnote: pageHeight > 0 && y < pageHeight*0.30 && size <= bodySize*0.92,
		})
	}
	for i := range rendered {
		if !rendered[i].header && !rendered[i].footnote {
			rendered[i].text = renderInlineRuns(rendered[i].line.runs, rendered[i].y, false)
		}
	}

	normalGap := dominantLineGap(rendered)
	bodyMargin := bodyLeftMargin(rendered, bodySize)
	for i := range rendered {
		rendered[i].heading = likelyHeading(rendered, i, bodySize, bodyMargin, normalGap)
	}

	var bodyBlocks []string
	var paragraph string
	flushParagraph := func() {
		if strings.TrimSpace(paragraph) != "" {
			bodyBlocks = append(bodyBlocks, strings.TrimSpace(paragraph))
		}
		paragraph = ""
	}

	previousBody := -1
	for i, line := range rendered {
		if line.header || line.footnote || line.text == "" {
			continue
		}
		if line.heading {
			flushParagraph()
			bodyBlocks = append(bodyBlocks, "## "+strings.TrimSpace(line.text))
			previousBody = -1
			continue
		}

		newParagraph := false
		if paragraph != "" && previousBody >= 0 {
			previous := rendered[previousBody]
			if line.x > bodyMargin+math.Max(8, bodySize) && endsParagraph(previous.plain) {
				newParagraph = true
			}
			if previous.y-line.y > normalGap*1.45 {
				newParagraph = true
			}
		}
		if newParagraph {
			flushParagraph()
		}
		paragraph = joinWrappedMarkdown(paragraph, line.text)
		previousBody = i
	}
	flushParagraph()

	footnotes := renderFootnotes(rendered)
	var out strings.Builder
	fmt.Fprintf(&out, "<!-- PDF page %d -->", pageNum)
	if len(bodyBlocks) > 0 {
		out.WriteString("\n\n")
		out.WriteString(strings.Join(bodyBlocks, "\n\n"))
	}
	if footnotes != "" {
		out.WriteString("\n\n")
		out.WriteString(footnotes)
	}
	return strings.TrimSpace(out.String())
}

func dominantFontSize(lines []positionedTextLine) float64 {
	weights := map[int]int{}
	for _, line := range lines {
		for _, run := range line.runs {
			if run.fontSize <= 0 {
				continue
			}
			key := int(math.Round(run.fontSize * 2))
			weights[key] += max(1, utf8.RuneCountInString(strings.TrimSpace(run.text)))
		}
	}
	bestKey, bestWeight := 0, -1
	for key, weight := range weights {
		if weight > bestWeight || weight == bestWeight && key > bestKey {
			bestKey, bestWeight = key, weight
		}
	}
	if bestKey == 0 {
		return 10
	}
	return float64(bestKey) / 2
}

func dominantLineGap(lines []renderedPositionedLine) float64 {
	var gaps []float64
	for i := 1; i < len(lines); i++ {
		if lines[i-1].header || lines[i-1].footnote || lines[i].header || lines[i].footnote {
			continue
		}
		gap := lines[i-1].y - lines[i].y
		if gap >= 4 && gap <= 30 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return 12
	}
	// The median is stable in typeset prose where normal leading dominates
	// occasional paragraph and heading gaps.
	return medianFloat(gaps)
}

func bodyLeftMargin(lines []renderedPositionedLine, bodySize float64) float64 {
	margin := math.Inf(1)
	for _, line := range lines {
		if line.header || line.footnote || math.Abs(line.fontSize-bodySize) > bodySize*0.20 {
			continue
		}
		margin = math.Min(margin, line.x)
	}
	if math.IsInf(margin, 1) {
		return 0
	}
	return margin
}

func likelyHeading(lines []renderedPositionedLine, i int, bodySize, bodyMargin, normalGap float64) bool {
	line := lines[i]
	if line.header || line.footnote || line.fontSize < bodySize*0.90 || line.x > bodyMargin+bodySize*0.75 {
		return false
	}
	plain := strings.TrimSpace(line.plain)
	last, _ := utf8.DecodeLastRuneInString(plain)
	if plain == "" || utf8.RuneCountInString(plain) > 120 || strings.ContainsRune(".,;:?!", last) {
		return false
	}

	prevGap, nextGap := 0.0, 0.0
	if i > 0 {
		prevGap = lines[i-1].y - line.y
	}
	if i+1 < len(lines) {
		nextGap = line.y - lines[i+1].y
	}
	firstAfterHeader := i == 0 || lines[i-1].header
	return firstAfterHeader && !strings.ContainsRune(plain, ',') && nextGap > normalGap*1.35 &&
		i+1 < len(lines) && lines[i+1].x > bodyMargin+math.Max(8, bodySize) ||
		!firstAfterHeader && prevGap > normalGap*1.35 && nextGap >= normalGap*0.90
}

func endsParagraph(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(text)
	return strings.ContainsRune(".!?\"'”’)]", last) || unicode.IsDigit(last)
}

func renderInlineRuns(runs []positionedTextRun, baseline float64, footnoteBody bool) string {
	var out strings.Builder
	previousVertical := inlineBaseline
	for i, run := range runs {
		text := run.text
		if text == "" {
			continue
		}

		trimmed := strings.TrimSpace(text)
		leading := text[:len(text)-len(strings.TrimLeftFunc(text, unicode.IsSpace))]
		trailing := text[len(strings.TrimRightFunc(text, unicode.IsSpace)):]
		vertical := classifyInlineRun(run, baseline, footnoteBody)
		if i > 0 && vertical == inlineBaseline && previousVertical != inlineSubscript &&
			needsInterRunSpace(out.String(), text) {
			out.WriteByte(' ')
		}
		out.WriteString(leading)

		switch vertical {
		case inlineSuperscript:
			out.WriteString("<sup>" + escapeMarkdown(trimmed) + "</sup>")
		case inlineSubscript:
			out.WriteString("<sub>" + escapeMarkdown(trimmed) + "</sub>")
		default:
			out.WriteString(styleMarkdownText(trimmed, run))
		}
		out.WriteString(trailing)
		previousVertical = vertical
	}
	return strings.TrimSpace(collapseSpaces(out.String()))
}

type inlineVertical uint8

const (
	inlineBaseline inlineVertical = iota
	inlineSuperscript
	inlineSubscript
)

func classifyInlineRun(run positionedTextRun, baseline float64, footnoteBody bool) inlineVertical {
	trimmed := strings.TrimSpace(run.text)
	if utf8.RuneCountInString(trimmed) > 4 {
		return inlineBaseline
	}
	verticalThreshold := math.Max(2, run.fontSize*0.40)
	if !footnoteBody && run.y > baseline+verticalThreshold {
		return inlineSuperscript
	}
	if run.y < baseline-verticalThreshold {
		return inlineSubscript
	}
	return inlineBaseline
}

func styleMarkdownText(text string, run positionedTextRun) string {
	prefix, marker, hasMarker := splitTrailingCitation(text)
	styled := escapeMarkdown(prefix)
	if run.bold && run.italic {
		styled = "***" + styled + "***"
	} else if run.bold {
		styled = "**" + styled + "**"
	} else if run.italic {
		styled = "*" + styled + "*"
	}
	if hasMarker {
		styled += "<sup>" + escapeMarkdown(marker) + "</sup>"
	}
	return styled
}

func splitTrailingCitation(text string) (string, string, bool) {
	runes := []rune(text)
	if len(runes) < 2 {
		return text, "", false
	}

	for markerLen := min(2, len(runes)-1); markerLen >= 1; markerLen-- {
		start := len(runes) - markerLen
		if !strings.ContainsRune(".,;:)]}", runes[start-1]) {
			continue
		}
		marker := string(runes[start:])
		if looksLikeTrailingCitationMarker(marker) {
			return string(runes[:start]), marker, true
		}
	}
	return text, "", false
}

func looksLikeTrailingCitationMarker(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 2 {
		return false
	}
	hasDigit := false
	for _, r := range text {
		if unicode.IsDigit(r) {
			hasDigit = true
			continue
		}
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return hasDigit || text == "T" || text == "t"
}

func escapeMarkdown(text string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
	)
	return replacer.Replace(text)
}

func joinRawRuns(runs []positionedTextRun) string {
	var out strings.Builder
	for i, run := range runs {
		if i > 0 && needsInterRunSpace(out.String(), run.text) {
			out.WriteByte(' ')
		}
		out.WriteString(run.text)
	}
	return collapseSpaces(out.String())
}

func needsInterRunSpace(previous, next string) bool {
	if previous == "" || next == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(previous)
	first, _ := utf8.DecodeRuneInString(next)
	if unicode.IsSpace(last) || unicode.IsSpace(first) {
		return false
	}
	return !strings.ContainsRune(".,;:!?)]}", first)
}

func joinWrappedMarkdown(paragraph, line string) string {
	line = strings.TrimSpace(line)
	if paragraph == "" {
		return line
	}
	if line == "" {
		return paragraph
	}
	if strings.HasSuffix(paragraph, "-") {
		first, _ := utf8.DecodeRuneInString(strings.TrimSpace(line))
		if unicode.IsLower(first) {
			return strings.TrimSuffix(paragraph, "-") + line
		}
	}
	return paragraph + " " + line
}

func renderFootnotes(lines []renderedPositionedLine) string {
	var blocks []string
	var marker, text string
	flush := func() {
		if strings.TrimSpace(text) == "" {
			marker, text = "", ""
			return
		}
		if marker == "" {
			blocks = append(blocks, "> "+strings.TrimSpace(text))
		} else {
			blocks = append(blocks, "<sup>"+escapeMarkdown(marker)+"</sup> "+strings.TrimSpace(text))
		}
		marker, text = "", ""
	}

	for _, line := range lines {
		if !line.footnote || len(line.line.runs) == 0 {
			continue
		}
		lineMarker, rest, ok := splitFootnoteLine(line.line)
		if ok {
			flush()
			marker = lineMarker
			text = rest
			continue
		}
		text = joinWrappedMarkdown(text, renderInlineRuns(line.line.runs, lineBaseline(line.line), true))
	}
	flush()
	return strings.Join(blocks, "\n\n")
}

func splitFootnoteLine(line positionedTextLine) (string, string, bool) {
	if len(line.runs) < 2 {
		return "", "", false
	}
	baseline := lineBaseline(line)
	first := line.runs[0]
	if first.y <= baseline+math.Max(2, first.fontSize*0.40) {
		return "", "", false
	}
	marker := strings.TrimSpace(first.text)
	if marker == "" || utf8.RuneCountInString(marker) > 3 {
		return "", "", false
	}
	return marker, renderInlineRuns(line.runs[1:], lineBaseline(positionedTextLine{runs: line.runs[1:]}), true), true
}

func pageDimensions(page pdfDict, f *pdfFile) (float64, float64) {
	box := f.getArray(f.inheritedPageValue(page, "CropBox"))
	if len(box) < 4 {
		box = f.getArray(f.inheritedPageValue(page, "MediaBox"))
	}
	if len(box) < 4 {
		return 0, 0
	}
	number := func(v any) float64 {
		n, _ := f.resolve(v).(pdfNumber)
		return float64(n)
	}
	return math.Abs(number(box[2]) - number(box[0])), math.Abs(number(box[3]) - number(box[1]))
}
