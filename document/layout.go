package document

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// word is a single whitespace-delimited token carrying its run's formatting.
// gap is the width of the single space that precedes it on a line (0 for the
// first word of a line or when no whitespace separated it from the previous
// token — e.g. a formatting change mid-word).
type word struct {
	text  string
	props RunProperties
	width float64
	gap   float64
	// lineBreak marks an explicit in-paragraph break (a zero-width marker,
	// never drawn); layoutParagraph ends the current line when it reaches one.
	// frame says whether the break also moves on to a new page or column.
	lineBreak bool
	frame     frameBreak
	// img is the inline picture this word draws instead of text; its width is
	// the picture's, and it sits on the baseline like a glyph.
	img *Image
	// field is the computed field (Run.Field) the word's text came from.
	field string
	// tab marks a <w:tab/>: it draws no text, and its width - the advance to
	// the next tab stop - is set when the line is packed. leader is the
	// character that stop fills the span with, or "".
	tab    bool
	leader string
	// link is the hyperlink target of the word's run (Run.Link), or "".
	link string
	// note is the footnote the word references (Run.Note), or nil.
	note *Note
	// hyphenBreak lets a line wrap before the word although no space precedes
	// it: it follows a hyphen inside a word, as in "інженеру-програмісту".
	hyphenBreak bool
	// asc and desc are how far the word's line reaches above and below the
	// baseline (its font's, short of a picture's); fontLine is its font's
	// line height, which a multiple line spacing takes its share of.
	asc, desc, fontLine float64
	// math is the laid-out equation the word draws instead of text.
	math *mathBox
	// box is the laid-out text box the word draws instead of text; like a
	// picture, it sits on the baseline.
	box *textBoxLayout
}

// height is what the word contributes to its line's height above the
// baseline: a picture's full height, an equation's ascent, or (for text) zero
// - text height comes from the line's font size.
func (w word) height() float64 {
	switch {
	case w.img != nil:
		return w.img.HeightPt
	case w.math != nil:
		return w.math.asc
	case w.box != nil:
		return w.box.h
	}
	return 0
}

// depth is how far the word reaches below the baseline beyond what its text
// would: an equation's descent (a fraction's denominator, say).
func (w word) depth() float64 {
	if w.math == nil {
		return 0
	}
	return w.math.desc
}

// frameBreak is an explicit break that moves what follows it to a new frame.
type frameBreak int

const (
	breakNone frameBreak = iota
	breakPage
	breakColumn
)

// line is a packed row of words ready to draw.
type line struct {
	words   []word
	natural float64 // words plus their internal gaps
	// ascent and descent are how far the line's fonts reach above and below
	// its baseline.
	ascent, descent float64
	maxImg          float64 // tallest inline image on the line (0 when text-only)
	height          float64 // vertical advance
	// breakBefore moves this line to the next page or column; only a flowing
	// paragraph acts on it - a table cell or header draws it as a plain line.
	breakBefore frameBreak
}

// lineHeightFor computes a line's vertical advance from the paragraph's
// resolved line spacing. natural is the line's single-spaced height: its
// fonts' line height (font), or more for an inline picture (maxImg tall) or
// an equation on it. OOXML's "auto" rule counts lines (w:line ÷ 240): a
// multiple adds that share of the font's line height below the text, so a
// picture is not stretched (LibreOffice's rule, which Word's output matches).
// Exact fixes the height (never below a picture); at-least floors it at the
// natural height; single uses the natural height.
func lineHeightFor(natural, font, maxImg float64, sp Spacing) float64 {
	switch sp.LineRule {
	case LineSpacingMultiple:
		return twips(max(natural+(sp.LineValue-1)*font, 0))
	case LineSpacingExact:
		return math.Max(sp.LineValue, maxImg)
	case LineSpacingAtLeast:
		return math.Max(natural, sp.LineValue)
	default: // LineSpacingSingle
		return natural
	}
}

// layoutParagraph packs a paragraph's runs into lines that fit width. A word
// wider than width is placed alone on its line and allowed to overflow rather
// than looping forever. Returns nil for an empty paragraph.
func layoutParagraph(r renderer, p Paragraph, width float64) []line {
	b := newLineBreaker(r, p)
	var lines []line
	for ln, ok := b.next(width); ok; ln, ok = b.next(width) {
		lines = append(lines, ln)
	}
	return lines
}

// lineBreaker packs a paragraph's words into lines one at a time, so that each
// line can be given its own width: the room a floating object beside it
// leaves. It is a plain value, so a copy taken before next can pack the same
// line again at another width.
type lineBreaker struct {
	props ParagraphProperties
	words []word
	r     renderer
	i     int // the next word to pack
	// started reports that a line has been produced: only the first is
	// shifted by the first-line indent.
	started bool
	// hardStart marks a line whose first word may keep its leading whitespace as
	// an indent: the paragraph's first line and any line after an explicit
	// <w:br/>. A soft-wrap continuation is not a hard start, so its leading
	// whitespace is dropped.
	hardStart bool
	pending   frameBreak // a page/column break waiting for the line after it
}

func newLineBreaker(r renderer, p Paragraph) lineBreaker {
	return lineBreaker{props: p.Props, words: measureWords(r, p), r: r, hardStart: true}
}

// done reports whether every line has been produced.
func (b *lineBreaker) done() bool { return b.i >= len(b.words) && b.pending == breakNone }

// next packs the next line into width, or reports false when there is none.
// The first line is drawn shifted by the first-line indent, so it has that
// much less room (or, under a hanging indent, more).
func (b *lineBreaker) next(width float64) (line, bool) {
	if !b.started {
		width -= b.props.Indent.FirstLineOffsetPt
	}
	var cur []word
	var curW, curAsc, curDesc, curImg, curFont float64
	// tabAsc and the rest are the tabs' font metrics, which size a line only
	// when it holds nothing else: Word does not raise a line for a tab.
	var tabAsc, tabDesc, tabFont float64
	flush := func() line {
		if curAsc == 0 && curImg == 0 {
			curAsc, curDesc, curFont = tabAsc, max(curDesc, tabDesc), tabFont
		}
		ln := line{
			words:       cur,
			natural:     curW,
			ascent:      curAsc,
			descent:     curDesc,
			maxImg:      curImg,
			height:      lineHeightFor(max(curAsc, curImg)+curDesc, curFont, curImg, b.props.Spacing),
			breakBefore: b.pending,
		}
		b.pending, b.started = breakNone, true
		return ln
	}
	blankLine := func() line {
		ln := line{height: emptyLineHeight(b.r, b.props), breakBefore: b.pending}
		b.pending, b.started = breakNone, true
		return ln
	}

	words := b.words
	for b.i < len(words) {
		i := b.i
		if w := words[i]; w.lineBreak {
			var ln line
			produced := true
			switch {
			case len(cur) > 0:
				ln = flush()
			case w.frame == breakNone || b.pending != breakNone:
				ln = blankLine() // a break on an empty line is a blank line
			default:
				produced = false
			}
			if w.frame != breakNone {
				b.pending = w.frame
			}
			b.hardStart = true
			b.i++
			if produced {
				return ln, true
			}
			continue
		}
		// A word Word split across runs (a formatting or revision change
		// mid-word) arrives as fragments with no gap between them; they wrap as
		// one unit, never with half the word on each line.
		// A tab is a unit of its own, and its advance depends on where the line
		// has got to - and, for a right or centered stop, on the text after it.
		end := i + 1
		for !words[i].tab && end < len(words) && !words[end].lineBreak && !words[end].tab && words[end].gap == 0 && !words[end].hyphenBreak {
			end++
		}
		var unitW float64
		for _, w := range words[i:end] {
			unitW += w.width
		}
		gap := words[i].gap
		if len(cur) == 0 && !b.hardStart {
			gap = 0 // drop leading whitespace at a soft-wrap continuation line
		}
		var leader string
		lineStart := b.props.Indent.LeftPt // where the line begins, from the column's left edge
		if !b.started {
			lineStart += b.props.Indent.FirstLineOffsetPt
		}
		if words[i].tab {
			after, toDecimal := segmentWidths(b.r, words[end:])
			var reach float64
			unitW, leader, reach = tabAdvance(b.props, lineStart+curW+gap, after, toDecimal)
			// Word lets the text run into the right margin up to a custom stop
			// set beyond it rather than wrapping (LibreOffice's TabOverMargin).
			width = max(width, reach-lineStart)
		}
		// A stop on the right margin (9355 dxa of a 9355 dxa column) is the
		// column's width give or take a rounding error of the twip-to-point
		// sums; text ending there still fits.
		if len(cur) > 0 && curW+gap+unitW > width+fitTolerancePt {
			return flush(), true // this word opens the next, soft-wrap line
		}
		for k, w := range words[i:end] {
			if k == 0 {
				w.gap = gap
			}
			if w.tab {
				w.width, w.leader = unitW, leader
			}
			cur = append(cur, w)
			curW += w.gap + w.width
			if w.tab {
				tabAsc, tabDesc, tabFont = max(tabAsc, w.asc), max(tabDesc, w.desc), max(tabFont, w.fontLine)
			} else {
				curAsc = math.Max(curAsc, w.asc)
				curFont = math.Max(curFont, w.fontLine)
				curDesc = max(curDesc, w.desc, w.depth())
			}
			curImg = math.Max(curImg, w.height())
		}
		b.hardStart = false
		b.i = end
	}
	if len(cur) > 0 {
		return flush(), true
	}
	if b.pending != breakNone {
		// A break ending the paragraph leaves its paragraph mark on the next
		// page, as Word draws it: an empty line there.
		return blankLine(), true
	}
	return line{}, false
}

// fitTolerancePt is how far a line may overrun its width and still fit: the
// widths are point sums of twip values, off by a float rounding error.
const fitTolerancePt = 0.01

// tabAdvance is how far a tab at x (measured from the text column's left edge)
// advances: to the nearest stop past x - a custom one, or the implicit stop a
// hanging indent puts at the left indent - less the text after it (after) for
// a right or centered stop, or the text up to its decimal separator
// (toDecimal) for a decimal one; with neither, to the next stop of the
// document's default tab grid. Bar stops are skipped: they only draw a rule.
// It also returns the leader the stop fills the span with, and reach: the
// position of the custom stop used, the text after which may extend that far
// even past the column's edge (0 for an implicit stop).
func tabAdvance(pp ParagraphProperties, x, after, toDecimal float64) (adv float64, leader string, reach float64) {
	const eps = 0.01 // a stop the pen is already on does not count
	next := TabStop{PosPt: math.Inf(1)}
	if i := slices.IndexFunc(pp.Tabs, func(s TabStop) bool { return s.Align != TabBar && s.PosPt > x+eps }); i >= 0 {
		next = pp.Tabs[i]
		reach = next.PosPt
	}
	if ind := pp.Indent; ind.FirstLineOffsetPt < 0 && ind.LeftPt > x+eps && ind.LeftPt < next.PosPt {
		next = TabStop{PosPt: ind.LeftPt}
		reach = 0
	}
	if math.IsInf(next.PosPt, 1) {
		step := cmp.Or(pp.DefaultTabStopPt, defaultTabStopPt)
		next = TabStop{PosPt: (math.Floor((x+eps)/step) + 1) * step}
	}
	adv = next.PosPt - x
	switch next.Align {
	case TabRight:
		adv -= after
	case TabDecimal:
		adv -= toDecimal
	case TabCenter:
		adv -= after / 2
	case TabLeft, TabBar:
	}
	return math.Max(adv, 0), next.Leader, reach
}

// segmentWidths measures the words up to the next tab or break - the text a
// right or centered tab stop aligns - and the part of it before its decimal
// separator, which a decimal stop aligns. With no separator the two are equal,
// so a decimal stop then ends the text at the stop like a right one.
func segmentWidths(r renderer, words []word) (whole, toDecimal float64) {
	toDecimal = -1
	for _, x := range words {
		if x.tab || x.lineBreak {
			break
		}
		if i := decimalSeparator(x.text); toDecimal < 0 && x.img == nil && i >= 0 {
			setRunFont(r, x.props)
			toDecimal = whole + x.gap + r.TextWidth(x.text[:i])
		}
		whole += x.gap + x.width
	}
	if toDecimal < 0 {
		toDecimal = whole
	}
	return whole, toDecimal
}

// decimalSeparator is the byte index of s's decimal separator: its last "." or
// "," with a digit on each side (so "1,234.50" aligns on the point and the
// Ukrainian "1 234,50" on the comma), or -1 when it has none.
func decimalSeparator(s string) int {
	for i := len(s) - 2; i > 0; i-- {
		if (s[i] == '.' || s[i] == ',') && isDigit(s[i-1]) && isDigit(s[i+1]) {
			return i
		}
	}
	return -1
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// drawLine renders one packed line at baseline y, positioned within [x0, x0+width]
// per the paragraph's alignment. isLast suppresses justification of a
// paragraph's final line. firstLineOffsetPt shifts only the first line
// (isFirst): positive for a first-line indent, negative for a hanging
// indent; it applies to AlignLeft/AlignJustify only - Word's first-line/
// hanging indent combined with center/right alignment is out of scope.
func drawLine(r renderer, ln line, align Alignment, x0, width, y float64, isLast bool, firstLineOffsetPt float64, isFirst bool) {
	// lead is a line's leading-whitespace indent (a hard-start line's first-word
	// gap; layout already zeroed it for soft-wrap continuations). It shifts
	// left/justify text right; centered/right-aligned text is positioned on its
	// content alone, so lead is subtracted from the alignment width and not drawn.
	var lead float64
	if len(ln.words) > 0 {
		lead = ln.words[0].gap
	}
	x := x0
	var extraPerGap float64
	switch align {
	case AlignRight:
		x = x0 + (width - (ln.natural - lead))
	case AlignCenter:
		x = x0 + (width-(ln.natural-lead))/2
	case AlignJustify:
		if !isLast {
			if gaps := len(stretchable(ln)); gaps > 0 {
				room := width
				if isFirst {
					room -= firstLineOffsetPt // the first line starts that much further in
				}
				if slack := room - ln.natural; slack > 0 {
					extraPerGap = slack / float64(gaps)
				}
			}
		}
		if isFirst {
			x += firstLineOffsetPt
		}
		x += lead
	default: // AlignLeft
		if isFirst {
			x += firstLineOffsetPt
		}
		x += lead
	}
	minX := x0
	if isFirst && firstLineOffsetPt < 0 { // a hanging indent legitimately starts left of x0
		minX = x0 + firstLineOffsetPt
	}
	if x < minX { // clamp an overflowing (oversized) line to its left edge
		x = minX
	}

	// An inline image sits on the baseline like a glyph, so a line taller than
	// its text pushes the baseline down to the image's foot rather than letting
	// the picture overflow the line box upward.
	baseline := y + math.Max(ln.ascent, ln.maxImg)
	placed := make([]float64, len(ln.words)) // each word's x, so shading can be drawn under it
	stretch := stretchable(ln)
	for i, w := range ln.words {
		if i > 0 {
			x += w.gap
			if extraPerGap > 0 && slices.Contains(stretch, i) {
				x += extraPerGap
			}
		}
		placed[i] = x
		x += w.width
	}

	// Backgrounds first, so text and pictures draw on top of their shading. A
	// run's shading covers the spaces inside it too, so a word whose predecessor
	// carries the same background extends its rectangle back over the gap
	// between them rather than leaving it unpainted.
	for i, w := range ln.words {
		if w.props.Shading == "" || w.img != nil {
			continue
		}
		left := placed[i]
		if i > 0 && ln.words[i-1].props.Shading == w.props.Shading && ln.words[i-1].img == nil {
			left = placed[i-1] + ln.words[i-1].width
		}
		r.FillRect(left, baseline-w.asc, placed[i]+w.width-left, w.asc+w.desc, w.props.Shading)
	}
	for i, w := range ln.words {
		if w.img != nil {
			drawPicture(r, w.img, placed[i], baseline-w.img.HeightPt)
			continue
		}
		if w.math != nil {
			w.math.paint(r, placed[i], baseline)
			continue
		}
		if w.box != nil {
			w.box.draw(r, placed[i], baseline-w.box.h)
			continue
		}
		if w.tab {
			drawLeader(r, w, placed[i], baseline)
			continue
		}
		setRunFont(r, w.props)
		r.SetTextColor(w.props.Color)
		r.DrawText(placed[i], baseline-w.props.BaselineShiftPt, w.text)
	}
	drawLinks(r, ln, placed, y, baseline)
}

// drawLinks makes each stretch of consecutive words sharing a hyperlink one
// clickable rectangle - the gaps between them included - as high as the line
// box at y, and at least as tall as the stretch's text above its baseline.
func drawLinks(r renderer, ln line, placed []float64, y, baseline float64) {
	for i := 0; i < len(ln.words); {
		link := ln.words[i].link
		j := i + 1
		for j < len(ln.words) && ln.words[j].link == link {
			j++
		}
		if link != "" {
			top := y
			for _, w := range ln.words[i:j] {
				top = math.Min(top, baseline-math.Max(w.asc, w.height()))
			}
			r.Link(placed[i], top, placed[j-1]+ln.words[j-1].width-placed[i], baseline+ln.descent-top, link)
		}
		i = j
	}
}

// drawPicture draws img at its drawn size with its top-left corner at (x, y):
// a cropped picture is drawn whole at the size that makes its visible part
// that big, clipped to that part.
func drawPicture(r renderer, img *Image, x, y float64) {
	c := img.Crop
	if c == (Crop{}) {
		r.DrawImage(x, y, img.WidthPt, img.HeightPt, img)
		return
	}
	visW, visH := 1-c.Left-c.Right, 1-c.Top-c.Bottom
	if visW <= 0.001 || visH <= 0.001 {
		return // cropped away entirely
	}
	w, h := img.WidthPt/visW, img.HeightPt/visH
	r.Clip(x, y, img.WidthPt, img.HeightPt)
	r.DrawImage(x-c.Left*w, y-c.Top*h, w, h, img)
	r.ClipEnd()
}

// barTabWidthPt is the thickness of a bar tab's rule: a hairline, as Word draws.
const barTabWidthPt = 0.5

// drawBarTabs draws the rule of each bar tab among tabs down a line box at y of
// height h; tab positions are measured from the text column's left edge colX.
func drawBarTabs(r renderer, tabs []TabStop, colX, y, h float64) {
	for _, t := range tabs {
		if t.Align == TabBar {
			r.StrokeLine(colX+t.PosPt, y, colX+t.PosPt, y+h, barTabWidthPt, "#000000")
		}
	}
}

// drawLeader fills a tab's span with its leader character, keeping half a
// character clear at each end so it does not touch the text around it.
func drawLeader(r renderer, w word, x, baseline float64) {
	if w.leader == "" {
		return
	}
	r.SetFont(w.props.FontFamily, w.props.Bold, w.props.Italic, false, false, w.props.SizePt)
	r.SetCharSpacing(0)
	r.SetTextColor(w.props.Color)
	cw := r.TextWidth(w.leader)
	if cw <= 0 {
		return
	}
	if n := int(w.width/cw) - 1; n > 0 {
		r.DrawText(x+(w.width-float64(n)*cw)/2, baseline, strings.Repeat(w.leader, n))
	}
}

// stretchable lists the words whose leading gap justification widens: those
// after the line's last tab, since Word lays out the text before a tab against
// its stop and only stretches what follows it.
func stretchable(ln line) []int {
	from := 1
	for i, w := range ln.words {
		if w.tab {
			from = i + 1
		}
	}
	var out []int
	for i := from; i < len(ln.words); i++ {
		if ln.words[i].gap > 0 {
			out = append(out, i)
		}
	}
	return out
}

// measureWords tokenizes a paragraph's runs into words, each carrying the
// measured width of the whitespace preceding it (its gap). A whitespace run is
// measured at its full width in its own font - preserved `xml:space="preserve"`
// spaces keep their width rather than collapsing to one - accumulating across
// consecutive whitespace tokens (e.g. spaces split by a formatting change).
func measureWords(r renderer, p Paragraph) []word {
	var words []word
	var pendingGap float64

	for _, run := range p.Runs {
		switch {
		case run.LineBreak, run.PageBreak, run.ColumnBreak:
			w := word{lineBreak: true}
			if run.PageBreak {
				w.frame = breakPage
			} else if run.ColumnBreak {
				w.frame = breakColumn
			}
			words = append(words, w)
			pendingGap = 0
			continue
		}
		// Whatever the run draws, its line is at least as tall as its font's.
		base := word{props: run.Props, gap: pendingGap, link: run.Link}
		base.asc, base.desc = fontExtent(r, run.Props)
		base.fontLine = base.asc + base.desc
		switch {
		case run.Tab:
			base.tab = true
		case run.TextBox != nil:
			box := layoutTextBox(r, *run.TextBox)
			base.box, base.width = &box, box.w
		case run.Math != nil:
			box := layoutMath(r, run.Math, run.Props)
			base.math, base.width = &box, box.w
		case run.Image != nil:
			base.img, base.width = run.Image, math.Max(run.Image.WidthPt, run.MinWidthPt)
		}
		if h := base.height(); base.img != nil || base.box != nil {
			// A picture stands on the baseline and fills the line down to
			// the font's bottom only as far as it is shorter than the font's
			// line: Word adds no descent below a taller one.
			base.desc = max(base.asc+base.desc-max(base.asc, h), 0)
		}
		if base.tab || base.box != nil || base.math != nil || base.img != nil {
			words = append(words, base)
			pendingGap = 0
			continue
		}
		for i, tok := range tokenize(run.Text) {
			setRunFont(r, run.Props)
			if tok.space {
				pendingGap += r.TextWidth(tok.text)
				continue
			}
			for j, part := range splitAfterHyphens(tok.text) {
				w := base
				w.text, w.width, w.gap, w.field = part, r.TextWidth(part), pendingGap, run.Field
				if i == 0 && j == 0 { // the run's advance floor applies to its first token (a list marker is one)
					w.width = math.Max(w.width, run.MinWidthPt)
					w.note = run.Note
				}
				if n := len(words); n > 0 && w.gap == 0 {
					w.hyphenBreak = hyphenBreak(words[n-1], part)
				}
				words = append(words, w)
				pendingGap = 0
			}
		}
	}
	return words
}

// twips rounds v to whole twips (1/20 pt), the unit Word and LibreOffice lay
// out in: a font's 16.0986pt line is 322 twips, 16.1pt, in both.
func twips(v float64) float64 { return math.Round(v*20) / 20 }

// emptyLineHeight is the height of an empty line of a paragraph: one line of
// its mark's font.
func emptyLineHeight(r renderer, pp ParagraphProperties) float64 {
	mark := pp.Mark
	if mark.SizePt <= 0 {
		mark.SizePt = defaultRenderSizePt
	}
	setRunFont(r, mark)
	_, h := r.FontMetrics()
	return lineHeightFor(h, h, 0, pp.Spacing)
}

// fontExtent is how far a line of the run's font reaches above and below the
// baseline.
func fontExtent(r renderer, props RunProperties) (asc, desc float64) {
	setRunFont(r, props)
	asc, h := r.FontMetrics()
	return asc, h - asc
}

// setRunFont makes the run's font and character spacing the renderer's.
func setRunFont(r renderer, p RunProperties) {
	r.SetFont(p.FontFamily, p.Bold, p.Italic, p.Underline, p.Strike, p.SizePt)
	r.SetCharSpacing(p.CharSpacingPt)
}

// splitAfterHyphens cuts s after each hyphen a line may wrap at: one inside
// the word with a letter after it (the break UAX #14 allows after a hyphen).
func splitAfterHyphens(s string) []string {
	var parts []string
	start := 0
	for i := 1; i < len(s)-1; i++ {
		if s[i] == '-' && startsWithLetter(s[i+1:]) {
			parts = append(parts, s[start:i+1])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// hyphenBreak reports whether a line may wrap between prev and text that
// follows it with no space: prev ends a word with a hyphen and a letter
// follows - which also holds for a word whose halves are in two runs.
func hyphenBreak(prev word, text string) bool {
	return prev.img == nil && !prev.tab && len(prev.text) > 1 && strings.HasSuffix(prev.text, "-") && startsWithLetter(text)
}

func startsWithLetter(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLetter(r)
}

type token struct {
	text  string
	space bool
}

// tokenize splits s into alternating runs of whitespace and non-whitespace.
// Whitespace runs collapse to a single space token (width is measured as one
// space); non-whitespace runs become word tokens.
func tokenize(s string) []token {
	var out []token
	start := 0
	inSpace := false
	initialized := false
	for i, r := range s {
		sp := unicode.IsSpace(r)
		if !initialized {
			inSpace = sp
			initialized = true
			continue
		}
		if sp != inSpace {
			out = append(out, token{text: s[start:i], space: inSpace})
			start = i
			inSpace = sp
		}
	}
	if initialized {
		out = append(out, token{text: s[start:], space: inSpace})
	}
	return out
}
