package document

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode"
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
}

// height is what the word contributes to its line's height: a picture's full
// height, or (for text) zero - text height comes from the line's font size.
func (w word) height() float64 {
	if w.img == nil {
		return 0
	}
	return w.img.HeightPt
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
	maxSize float64 // tallest run size on the line
	maxImg  float64 // tallest inline image on the line (0 when text-only)
	height  float64 // vertical advance
	// breakBefore moves this line to the next page or column; only a flowing
	// paragraph acts on it - a table cell or header draws it as a plain line.
	breakBefore frameBreak
}

// lineHeightFor computes a line's vertical advance from the paragraph's
// resolved line spacing. The natural single-spaced height of a line is its
// tallest run size times the default multiplier (lineSpacing), floored at the
// tallest inline image so a picture is never clipped by the line it sits on;
// OOXML's "auto" rule counts that natural height in lines (w:line ÷ 240), so a
// multiple scales the natural height rather than the bare font size. Exact fixes
// the height; at-least floors it at the natural height; single uses the natural
// height.
func lineHeightFor(maxSize, maxImg float64, sp Spacing) float64 {
	natural := math.Max(maxSize*lineSpacing, maxImg)
	switch sp.LineRule {
	case LineSpacingMultiple:
		return natural * sp.LineValue
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
func (b *lineBreaker) next(width float64) (line, bool) {
	var cur []word
	var curW, curMax, curImg float64
	flush := func() line {
		ln := line{
			words:       cur,
			natural:     curW,
			maxSize:     curMax,
			maxImg:      curImg,
			height:      lineHeightFor(curMax, curImg, b.props.Spacing),
			breakBefore: b.pending,
		}
		b.pending, b.started = breakNone, true
		return ln
	}
	blankLine := func() line {
		ln := line{height: lineHeightFor(defaultRenderSizePt, 0, b.props.Spacing), breakBefore: b.pending}
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
		for !words[i].tab && end < len(words) && !words[end].lineBreak && !words[end].tab && words[end].gap == 0 {
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
			unitW, leader = tabAdvance(b.props, lineStart+curW+gap, after, toDecimal)
		}
		if len(cur) > 0 && curW+gap+unitW > width {
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
			curMax = math.Max(curMax, w.props.SizePt)
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

// tabAdvance is how far a tab at x (measured from the text column's left edge)
// advances: to the nearest stop past x - a custom one, or the implicit stop a
// hanging indent puts at the left indent - less the text after it (after) for
// a right or centered stop, or the text up to its decimal separator
// (toDecimal) for a decimal one; with neither, to the next stop of the
// document's default tab grid. Bar stops are skipped: they only draw a rule.
// It also returns the leader the stop fills the span with.
func tabAdvance(pp ParagraphProperties, x, after, toDecimal float64) (float64, string) {
	const eps = 0.01 // a stop the pen is already on does not count
	next := TabStop{PosPt: math.Inf(1)}
	if i := slices.IndexFunc(pp.Tabs, func(s TabStop) bool { return s.Align != TabBar && s.PosPt > x+eps }); i >= 0 {
		next = pp.Tabs[i]
	}
	if ind := pp.Indent; ind.FirstLineOffsetPt < 0 && ind.LeftPt > x+eps && ind.LeftPt < next.PosPt {
		next = TabStop{PosPt: ind.LeftPt}
	}
	if math.IsInf(next.PosPt, 1) {
		step := cmp.Or(pp.DefaultTabStopPt, defaultTabStopPt)
		next = TabStop{PosPt: (math.Floor((x+eps)/step) + 1) * step}
	}
	adv := next.PosPt - x
	switch next.Align {
	case TabRight:
		adv -= after
	case TabDecimal:
		adv -= toDecimal
	case TabCenter:
		adv -= after / 2
	case TabLeft, TabBar:
	}
	return math.Max(adv, 0), next.Leader
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
			r.SetFont(x.props.FontFamily, x.props.Bold, x.props.Italic, x.props.Underline, x.props.Strike, x.props.SizePt)
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
				if slack := width - ln.natural; slack > 0 {
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
	baseline := y + math.Max(ln.maxSize, ln.maxImg)
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
		top := baseline - w.props.SizePt
		r.FillRect(left, top, placed[i]+w.width-left, w.props.SizePt*lineSpacing, w.props.Shading)
	}
	for i, w := range ln.words {
		if w.img != nil {
			drawPicture(r, w.img, placed[i], baseline-w.img.HeightPt)
			continue
		}
		if w.tab {
			drawLeader(r, w, placed[i], baseline)
			continue
		}
		r.SetFont(w.props.FontFamily, w.props.Bold, w.props.Italic, w.props.Underline, w.props.Strike, w.props.SizePt)
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
				top = math.Min(top, baseline-math.Max(w.props.SizePt, w.height()))
			}
			r.Link(placed[i], top, placed[j-1]+ln.words[j-1].width-placed[i], baseline+ln.maxSize*(lineSpacing-1)-top, link)
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
		case run.Tab:
			words = append(words, word{tab: true, props: run.Props, gap: pendingGap, link: run.Link})
			pendingGap = 0
			continue
		case run.Image != nil:
			words = append(words, word{
				img:   run.Image,
				props: run.Props,
				width: math.Max(run.Image.WidthPt, run.MinWidthPt),
				gap:   pendingGap,
				link:  run.Link,
			})
			pendingGap = 0
			continue
		}
		for i, tok := range tokenize(run.Text) {
			r.SetFont(run.Props.FontFamily, run.Props.Bold, run.Props.Italic, run.Props.Underline, run.Props.Strike, run.Props.SizePt)
			if tok.space {
				pendingGap += r.TextWidth(tok.text)
				continue
			}
			w := word{text: tok.text, props: run.Props, width: r.TextWidth(tok.text), gap: pendingGap, field: run.Field, link: run.Link}
			if i == 0 { // the run's advance floor applies to its first token (a list marker is one)
				w.width = math.Max(w.width, run.MinWidthPt)
				w.note = run.Note
			}
			words = append(words, w)
			pendingGap = 0
		}
	}
	return words
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
