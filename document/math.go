package document

import (
	"cmp"
	"encoding/xml"
	"math"
	"strings"
	"unicode"
)

// mathNS is the namespace of Office Math (OMML): m:oMathPara, m:oMath and the
// structures inside them.
const mathNS = "http://schemas.openxmlformats.org/officeDocument/2006/math"

// mathFont is the family equations are set in, as Word does; without the font
// itself it maps to the embedded serif.
const mathFont = "Cambria Math"

// Math is an equation (m:oMath), drawn within its line like an inline picture.
type Math struct {
	// Display marks an equation in a paragraph of its own (m:oMathPara), whose
	// large operators are drawn at display size.
	Display bool
	root    xmlNode
}

// xmlNode is an element kept as a generic tree. An equation is read this way:
// OMML has dozens of small element kinds, walked by name when laid out.
type xmlNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Kids    []xmlNode  `xml:",any"`
}

// kid is n's first OMML child named local, or nil.
func (n *xmlNode) kid(local string) *xmlNode {
	if n == nil {
		return nil
	}
	for i := range n.Kids {
		if k := &n.Kids[i]; k.XMLName.Space == mathNS && k.XMLName.Local == local {
			return k
		}
	}
	return nil
}

// prop reads a property of n: the m:val of the element named local inside its
// property element pr (m:fPr/m:type, say), and whether that element is there.
func (n *xmlNode) prop(pr, local string) (string, bool) {
	k := n.kid(pr).kid(local)
	if k == nil {
		return "", false
	}
	for _, a := range k.Attrs {
		if a.Name.Local == "val" {
			return a.Value, true
		}
	}
	return "", true
}

// flag reads an on/off property: on when declared without a value or with an
// on value.
func (n *xmlNode) flag(pr, local string) bool {
	v, ok := n.prop(pr, local)
	return ok && (&xmlOnOff{Val: v}).on()
}

// args lays out n's child named local as a row (nil child: an empty box).
func (st mathStyle) args(r renderer, n *xmlNode, local string) mathBox {
	k := n.kid(local)
	if k == nil {
		return mathBox{}
	}
	return st.row(r, k.Kids)
}

// mathBox is a laid-out piece of an equation: its advance width, its extent
// above and below the baseline, and how to draw it with its baseline at base.
type mathBox struct {
	w, asc, desc float64
	draw         func(r renderer, x, base float64)
}

func (b mathBox) paint(r renderer, x, base float64) {
	if b.draw != nil {
		b.draw(r, x, base)
	}
}

// hbox sets boxes side by side on a common baseline.
func hbox(boxes ...mathBox) mathBox {
	var out mathBox
	for _, b := range boxes {
		out.w += b.w
		out.asc = max(out.asc, b.asc)
		out.desc = max(out.desc, b.desc)
	}
	out.draw = func(r renderer, x, base float64) {
		for _, b := range boxes {
			b.paint(r, x, base)
			x += b.w
		}
	}
	return out
}

// space is an empty box w wide.
func space(w float64) mathBox { return mathBox{w: w} }

// Metrics, as fractions of the font size: the extent of a line of text above
// and below its baseline, and the math axis - the height of a fraction bar or
// a minus sign above the baseline.
const (
	mathAscent  = 0.7
	mathDescent = 0.22
	mathAxis    = 0.25
)

// mathStyle is the formatting an equation's part is laid out in.
type mathStyle struct {
	props   RunProperties // font and color; SizePt is the current size
	display bool          // large operators at display size
	upright bool          // letters upright, as in a function name
	tight   bool          // in a script or limit: operators set without spacing
}

func (st mathStyle) size() float64 { return st.props.SizePt }

// ink is the color strokes are drawn in.
func (st mathStyle) ink() string { return cmp.Or(st.props.Color, "#000000") }

// rule is the thickness of the equation's lines (fraction bar, radical).
func (st mathStyle) rule() float64 { return max(0.4, st.size()*0.05) }

// script is the style of a sub- or superscript, a limit or a radical's degree.
func (st mathStyle) script() mathStyle {
	st.props.SizePt = max(st.props.SizePt*0.7, 4)
	st.display, st.upright, st.tight = false, false, true
	return st
}

func (st mathStyle) font(r renderer, italic, bold bool, size float64) {
	r.SetFont(st.props.FontFamily, bold, italic, false, false, size)
	r.SetCharSpacing(0)
}

// glyphs is a box of text set in the current size.
func (st mathStyle) glyphs(r renderer, s string, italic, bold bool) mathBox {
	size := st.size()
	st.font(r, italic, bold, size)
	return mathBox{
		w: r.TextWidth(s), asc: size * mathAscent, desc: size * mathDescent,
		draw: func(r renderer, x, base float64) {
			st.font(r, italic, bold, size)
			r.SetTextColor(st.props.Color)
			r.DrawText(x, base, s)
		},
	}
}

// layoutMath lays out an equation in the formatting of the run it stands in.
func layoutMath(r renderer, m *Math, props RunProperties) mathBox {
	props.FontFamily = mathFont
	props.Bold, props.Italic, props.Underline, props.Strike = false, false, false, false
	props.CharSpacingPt, props.BaselineShiftPt = 0, 0
	st := mathStyle{props: props, display: m.Display}
	return st.row(r, m.root.Kids)
}

// atom is the spacing class of the last thing set in a row: a binary operator
// after another operator (or at the start) is a sign, and gets no spacing.
type atom int

const (
	atomNone atom = iota
	atomOrd
	atomBin
	atomRel
	atomOpen
	atomPunct
)

// row lays out a sequence of math elements and runs.
func (st mathStyle) row(r renderer, kids []xmlNode) mathBox {
	var boxes []mathBox
	prev := atomNone
	for i := range kids {
		k := &kids[i]
		if k.XMLName.Space != mathNS && k.XMLName.Space != wordNS || strings.HasSuffix(k.XMLName.Local, "Pr") {
			continue // properties, and foreign markup such as bookmarks
		}
		if k.XMLName.Local == "r" {
			var rb []mathBox
			rb, prev = st.text(r, k, prev)
			boxes = append(boxes, rb...)
			continue
		}
		boxes = append(boxes, st.elem(r, k))
		prev = atomOrd
	}
	return hbox(boxes...)
}

// text lays out a math run (m:r): letters in italic unless its style (m:sty)
// or normal-text flag (m:nor) says otherwise, operators spaced as Word spaces
// them. It returns the boxes and the class of the last character.
func (st mathStyle) text(r renderer, n *xmlNode, prev atom) ([]mathBox, atom) {
	var sb strings.Builder
	for _, k := range n.Kids {
		if k.XMLName.Local == "t" {
			sb.WriteString(k.Text)
		}
	}
	sty, hasSty := n.prop("rPr", "sty")
	normal := n.flag("rPr", "nor")
	bold := sty == "b" || sty == "bi"
	if normal {
		return []mathBox{st.glyphs(r, sb.String(), false, bold)}, atomOrd
	}

	size := st.size()
	var boxes []mathBox
	var seg strings.Builder
	segItalic := false
	flush := func() {
		if seg.Len() > 0 {
			boxes = append(boxes, st.glyphs(r, seg.String(), segItalic, bold))
			seg.Reset()
		}
	}
	for _, c := range sb.String() {
		if unicode.IsSpace(c) {
			continue // math ignores the spaces typed in it
		}
		switch c {
		case '-':
			c = '−'
		case '\'':
			c = '′'
		}
		class := atomClass(c)
		if class == atomBin && (prev == atomNone || prev == atomBin || prev == atomRel || prev == atomOpen || prev == atomPunct) {
			class = atomOrd // a sign, as in "-b"
		}
		var before, after float64
		switch {
		case st.tight:
			// Scripts and limits set their operators tight ("i=1").
		case class == atomBin:
			before, after = size*4/18, size*4/18
		case class == atomRel:
			before, after = size*5/18, size*5/18
		case class == atomPunct:
			after = size * 3 / 18
		}
		// Greek capitals stay upright, as in Word.
		italic := unicode.IsLetter(c) && !st.upright && (!unicode.Is(unicode.Greek, c) || !unicode.IsUpper(c))
		if hasSty {
			italic = sty == "i" || sty == "bi"
		}
		if before > 0 || after > 0 || italic != segItalic {
			flush()
		}
		segItalic = italic
		if before > 0 || after > 0 {
			boxes = append(boxes, space(before), st.glyphs(r, string(c), false, bold), space(after))
		} else {
			seg.WriteRune(c)
		}
		prev = class
	}
	flush()
	return boxes, prev
}

// atomClass is the spacing class of a character.
func atomClass(c rune) atom {
	switch {
	case strings.ContainsRune("+−±∓×÷·∘∗⋅∪∩∧∨⊕⊗", c):
		return atomBin
	case strings.ContainsRune("=<>≤≥≠≈≡∼≅∝→←↔⇒⇐⇔∈∉∋⊂⊃⊆⊇≪≫∣:", c):
		return atomRel
	case strings.ContainsRune("([{⟨", c):
		return atomOpen
	case strings.ContainsRune(",;", c):
		return atomPunct
	default:
		return atomOrd
	}
}

// elem lays out one math structure. An unknown one shows its arguments in a
// row, so its text is never lost.
func (st mathStyle) elem(r renderer, n *xmlNode) mathBox {
	switch n.XMLName.Local {
	case "f":
		return st.fraction(r, n)
	case "rad":
		return st.radical(r, n)
	case "sSup", "sSub", "sSubSup", "sPre":
		base := st.args(r, n, "e")
		return st.scripts(r, base, n.kid("sub"), n.kid("sup"), n.XMLName.Local == "sPre")
	case "d":
		return st.delimited(r, n)
	case "nary":
		return st.nary(r, n)
	case "func":
		name := st
		name.upright = true
		return hbox(name.args(r, n, "fName"), space(st.size()/6), st.args(r, n, "e"))
	case "limLow", "limUpp":
		return st.limit(r, n)
	case "acc":
		return st.accent(r, n)
	case "bar":
		return st.bar(r, n)
	case "borderBox":
		return st.borderBox(r, n)
	case "groupChr":
		return st.groupChr(r, n)
	case "m":
		var rows [][]mathBox
		for i := range n.Kids {
			if mr := &n.Kids[i]; mr.XMLName.Local == "mr" {
				rows = append(rows, st.cells(r, mr))
			}
		}
		return st.grid(rows)
	case "eqArr":
		var rows [][]mathBox
		for _, b := range st.cells(r, n) {
			rows = append(rows, []mathBox{b})
		}
		return st.grid(rows)
	case "phant":
		b := st.args(r, n, "e")
		if v, ok := n.prop("phantPr", "show"); ok && !(&xmlOnOff{Val: v}).on() {
			b.draw = nil
		}
		if n.flag("phantPr", "zeroWid") {
			b.w = 0
		}
		if n.flag("phantPr", "zeroAsc") {
			b.asc = 0
		}
		if n.flag("phantPr", "zeroDesc") {
			b.desc = 0
		}
		return b
	default: // m:box, m:e and the like: their content in a row
		return st.row(r, n.Kids)
	}
}

// cells lays out each m:e child of n.
func (st mathStyle) cells(r renderer, n *xmlNode) []mathBox {
	var out []mathBox
	for i := range n.Kids {
		if e := &n.Kids[i]; e.XMLName.Space == mathNS && e.XMLName.Local == "e" {
			out = append(out, st.row(r, e.Kids))
		}
	}
	return out
}

// fraction lays out m:f: stacked over a bar on the math axis, stacked without
// one (noBar), or in a line with a slash (lin, skw).
func (st mathStyle) fraction(r renderer, n *xmlNode) mathBox {
	num, den := st.args(r, n, "num"), st.args(r, n, "den")
	typ, _ := n.prop("fPr", "type")
	if typ == "lin" || typ == "skw" {
		return hbox(num, st.glyphs(r, "/", false, false), den)
	}
	s, t := st.size(), st.rule()
	axis, gap, pad := s*mathAxis, s*0.12, s*0.1
	w := max(num.w, den.w) + 2*pad
	up := axis + t/2 + gap + num.desc // num's baseline above ours
	down := gap + t/2 + den.asc - axis
	bar := typ != "noBar"
	return mathBox{
		w: w, asc: up + num.asc, desc: down + den.desc,
		draw: func(r renderer, x, base float64) {
			num.paint(r, x+(w-num.w)/2, base-up)
			den.paint(r, x+(w-den.w)/2, base+down)
			if bar {
				r.StrokeLine(x+pad/2, base-axis, x+w-pad/2, base-axis, t, st.ink())
			}
		},
	}
}

// radical lays out m:rad: the radical sign drawn in strokes as tall as its
// argument, with the degree (unless hidden) in the crook of its tick.
func (st mathStyle) radical(r renderer, n *xmlNode) mathBox {
	e := st.args(r, n, "e")
	s, t := st.size(), st.rule()
	e.asc, e.desc = max(e.asc, s*mathAscent), max(e.desc, s*mathDescent)
	top := e.asc + s*0.1 + t  // the overline above the baseline
	bottom := e.desc + s*0.05 // the sign's foot below it
	h := top + bottom
	signW := s*0.45 + h*0.1
	var deg mathBox
	if !n.flag("radPr", "degHide") {
		deg = st.script().args(r, n, "deg")
	}
	degRaise := 0.6*h - bottom // degree's baseline above ours
	shift := max(deg.w-signW*0.5, 0)
	w := shift + signW + s*0.05 + e.w + s*0.1
	return mathBox{
		w: w, asc: max(top, degRaise+deg.asc), desc: bottom,
		draw: func(r renderer, x, base float64) {
			ink := st.ink()
			xs, yb, yt := x+shift, base+bottom, base-top+t/2
			p1x, p1y := xs, yb-0.42*h
			p2x, p2y := xs+0.22*signW, yb-0.5*h
			p3x := xs + 0.5*signW
			p4x := xs + signW
			r.StrokeLine(p1x, p1y, p2x, p2y, t, ink)
			r.StrokeLine(p2x, p2y, p3x, yb, 2*t, ink)
			r.StrokeLine(p3x, yb, p4x, yt, t, ink)
			r.StrokeLine(p4x, yt, x+w, yt, t, ink)
			deg.paint(r, xs+signW*0.5-deg.w, base-degRaise)
			e.paint(r, p4x+s*0.05, base)
		},
	}
}

// scripts attaches a subscript and/or superscript (either may be nil) to base:
// after it, or before it (pre, m:sPre).
func (st mathStyle) scripts(r renderer, base mathBox, subN, supN *xmlNode, pre bool) mathBox {
	s, sc := st.size(), st.script()
	var sub, sup mathBox
	if subN != nil {
		sub = sc.row(r, subN.Kids)
	}
	if supN != nil {
		sup = sc.row(r, supN.Kids)
	}
	up := max(0.42*s, base.asc-0.3*s, sup.desc+0.25*s)
	down := max(0.2*s, sub.asc-0.45*s, base.desc-0.5*sub.asc)
	if subN != nil && supN != nil {
		if gap := (down - sub.asc) + (up - sup.desc); gap < 0.15*s {
			down += 0.15*s - gap
		}
	}
	pad := 0.05 * s
	sw := max(sub.w, sup.w)
	out := mathBox{w: base.w + pad + sw, asc: base.asc, desc: base.desc}
	if supN != nil {
		out.asc = max(out.asc, up+sup.asc)
	}
	if subN != nil {
		out.desc = max(out.desc, down+sub.desc)
	}
	out.draw = func(r renderer, x, b float64) {
		bx, sx := x, x+base.w+pad
		if pre {
			bx, sx = x+sw+pad, x
		}
		base.paint(r, bx, b)
		if supN != nil {
			sup.paint(r, sx+alignRight(pre, sw, sup.w), b-up)
		}
		if subN != nil {
			sub.paint(r, sx+alignRight(pre, sw, sub.w), b+down)
		}
	}
	return out
}

// alignRight is how far a script w wide is moved in a column sw wide: to its
// right edge before the base (pre), else not at all.
func alignRight(pre bool, sw, w float64) float64 {
	if pre {
		return sw - w
	}
	return 0
}

// delimited lays out m:d: its arguments between the opening and closing
// characters, separated by the separator, all as tall as the content.
func (st mathStyle) delimited(r renderer, n *xmlNode) mathBox {
	beg, end, sep := "(", ")", "|"
	if v, ok := n.prop("dPr", "begChr"); ok {
		beg = v
	}
	if v, ok := n.prop("dPr", "endChr"); ok {
		end = v
	}
	if v, ok := n.prop("dPr", "sepChr"); ok {
		sep = v
	}
	args := st.cells(r, n)
	s := st.size()
	axis := s * mathAxis
	var reach float64 // the content's extent from the axis, either way
	for _, a := range args {
		reach = max(reach, a.asc-axis, a.desc+axis)
	}
	var boxes []mathBox
	if beg != "" {
		boxes = append(boxes, st.fence(r, beg, reach))
	}
	for i, a := range args {
		if i > 0 && sep != "" {
			boxes = append(boxes, st.fence(r, sep, reach))
		}
		boxes = append(boxes, a)
	}
	if end != "" {
		boxes = append(boxes, st.fence(r, end, reach))
	}
	return hbox(boxes...)
}

// fence is a delimiter reaching reach above and below the math axis: the
// font's glyph while content is no taller than text, else drawn in strokes.
func (st mathStyle) fence(r renderer, ch string, reach float64) mathBox {
	s := st.size()
	if reach <= s*(mathAscent-mathAxis)*1.05 {
		return st.glyphs(r, ch, false, false)
	}
	axis, t := s*mathAxis, st.rule()*1.2
	h := 2 * reach
	w := min(s*0.28+h*0.04, s*0.6)
	switch ch {
	case "|":
		w = s * 0.25
	case "‖":
		w = s * 0.4
	}
	return mathBox{
		w: w, asc: axis + reach, desc: reach - axis,
		draw: func(r renderer, x, base float64) {
			yt, yb := base-axis-reach, base-axis+reach
			xl, xr := x+s*0.08, x+w-s*0.08
			strokeFence(r, ch, xl, xr, yt, yb, t, st.ink())
		},
	}
}

// strokeFence draws a bracket in the box [xl, xr]×[yt, yb] as a polyline.
// A character with no stroked shape is drawn as its glyph stretched to fit.
func strokeFence(r renderer, ch string, xl, xr, yt, yb, t float64, ink string) {
	ym, xm := (yt+yb)/2, (xl+xr)/2
	mirror := func(pts [][2]float64) [][2]float64 {
		out := make([][2]float64, len(pts))
		for i, p := range pts {
			out[i] = [2]float64{xl + xr - p[0], p[1]}
		}
		return out
	}
	var pts [][2]float64
	right := strings.ContainsRune(")]}⟩〉⌉⌋", []rune(ch)[0])
	switch ch {
	case "(", ")":
		const steps, tm = 12, 1.0 // the arc's half-angle, in radians
		for i := 0; i <= steps; i++ {
			th := -tm + 2*tm*float64(i)/steps
			pts = append(pts, [2]float64{
				xr - (xr-xl)*(math.Cos(th)-math.Cos(tm))/(1-math.Cos(tm)),
				ym + (yb-yt)/2*math.Sin(th)/math.Sin(tm),
			})
		}
	case "[", "]":
		pts = [][2]float64{{xr, yt}, {xl, yt}, {xl, yb}, {xr, yb}}
	case "{", "}":
		d := (yb - yt) * 0.06
		pts = [][2]float64{{xr, yt}, {xm, yt + d}, {xm, ym - d}, {xl, ym}, {xm, ym + d}, {xm, yb - d}, {xr, yb}}
	case "⟨", "⟩", "〈", "〉", "<", ">":
		pts = [][2]float64{{xr, yt}, {xl, ym}, {xr, yb}}
		right = ch == "⟩" || ch == "〉" || ch == ">"
	case "⌈", "⌉":
		pts = [][2]float64{{xr, yt}, {xl, yt}, {xl, yb}}
	case "⌊", "⌋":
		pts = [][2]float64{{xl, yt}, {xl, yb}, {xr, yb}}
	case "|":
		pts = [][2]float64{{xm, yt}, {xm, yb}}
	case "‖":
		r.StrokeLine(xl+(xr-xl)*0.3, yt, xl+(xr-xl)*0.3, yb, t, ink)
		r.StrokeLine(xl+(xr-xl)*0.7, yt, xl+(xr-xl)*0.7, yb, t, ink)
		return
	default:
		size := (yb - yt) / (mathAscent + mathDescent)
		r.SetFont(mathFont, false, false, false, false, size)
		r.SetTextColor(ink)
		r.DrawText(xl, yb-mathDescent*size, ch)
		return
	}
	if right {
		pts = mirror(pts)
	}
	for i := 1; i < len(pts); i++ {
		r.StrokeLine(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1], t, ink)
	}
}

// integrals are the n-ary operators whose limits sit beside them by default.
const integrals = "∫∬∭∮∯∰∱∲∳"

// nary lays out m:nary: a large operator, its limits (under and over it, or
// as scripts beside it) and its argument.
func (st mathStyle) nary(r renderer, n *xmlNode) mathBox {
	chr := "∫"
	if v, ok := n.prop("naryPr", "chr"); ok && v != "" {
		chr = v
	}
	integral := strings.Contains(integrals, chr)
	loc, ok := n.prop("naryPr", "limLoc")
	if !ok {
		loc = "undOvr"
		if integral {
			loc = "subSup"
		}
	}
	s := st.size()
	opSize := s
	if st.display {
		opSize = s * 1.4
		if integral {
			opSize = s * 1.6
		}
	}
	big := st
	big.props.SizePt = opSize
	op := big.glyphs(r, chr, false, false)
	// The glyph is centered on the math axis.
	lift := s*mathAxis - opSize*(mathAscent-mathDescent)/2
	op = mathBox{w: op.w, asc: op.asc + lift, desc: op.desc - lift, draw: func(r renderer, x, base float64) {
		big.glyphs(r, chr, false, false).paint(r, x, base-lift)
	}}

	var subN, supN *xmlNode
	if !n.flag("naryPr", "subHide") {
		subN = n.kid("sub")
	}
	if !n.flag("naryPr", "supHide") {
		supN = n.kid("sup")
	}
	var withLimits mathBox
	if loc == "subSup" {
		withLimits = st.scripts(r, op, subN, supN, false)
	} else {
		withLimits = st.stackLimits(r, op, subN, supN)
	}
	return hbox(withLimits, space(s/6), st.args(r, n, "e"))
}

// stackLimits centers limits under and over base (either may be nil).
func (st mathStyle) stackLimits(r renderer, base mathBox, underN, overN *xmlNode) mathBox {
	s, sc := st.size(), st.script()
	var under, over mathBox
	if underN != nil {
		under = sc.row(r, underN.Kids)
	}
	if overN != nil {
		over = sc.row(r, overN.Kids)
	}
	gap := s * 0.1
	w := max(base.w, under.w, over.w)
	up := base.asc + gap + over.desc
	down := base.desc + gap + under.asc
	out := mathBox{w: w, asc: base.asc, desc: base.desc}
	if overN != nil {
		out.asc = up + over.asc
	}
	if underN != nil {
		out.desc = down + under.desc
	}
	out.draw = func(r renderer, x, b float64) {
		base.paint(r, x+(w-base.w)/2, b)
		if overN != nil {
			over.paint(r, x+(w-over.w)/2, b-up)
		}
		if underN != nil {
			under.paint(r, x+(w-under.w)/2, b+down)
		}
	}
	return out
}

// limit lays out m:limLow and m:limUpp: the argument with a limit centered
// under or over it.
func (st mathStyle) limit(r renderer, n *xmlNode) mathBox {
	e := st.args(r, n, "e")
	if n.XMLName.Local == "limLow" {
		return st.stackLimits(r, e, n.kid("lim"), nil)
	}
	return st.stackLimits(r, e, nil, n.kid("lim"))
}

// spacingAccents maps a combining accent (m:acc's character) to the glyph
// drawn over the argument.
var spacingAccents = map[string]string{
	"̀": "`", "́": "´", "̂": "ˆ", "̃": "˜", "̆": "˘",
	"̇": "˙", "̈": "¨", "̌": "ˇ", "⃖": "←", "⃗": "→", "⃑": "→",
}

// accent lays out m:acc: a mark centered over its argument; an overline
// accent is drawn as a line.
func (st mathStyle) accent(r renderer, n *xmlNode) mathBox {
	e := st.args(r, n, "e")
	s := st.size()
	chr := "̂"
	if v, ok := n.prop("accPr", "chr"); ok && v != "" {
		chr = v
	}
	e.asc = max(e.asc, s*mathAscent)
	out := mathBox{w: e.w, asc: e.asc + s*0.3, desc: e.desc}
	if chr == "̄" || chr == "̅" || chr == "¯" {
		out.draw = func(r renderer, x, base float64) {
			e.paint(r, x, base)
			y := base - e.asc - s*0.1
			r.StrokeLine(x, y, x+e.w, y, st.rule(), st.ink())
		}
		return out
	}
	glyph, arrow := cmp.Or(spacingAccents[chr], chr), chr == "⃖" || chr == "⃗" || chr == "⃑"
	mark := st
	raise := e.asc - s*0.5 + s*0.05 // spacing accents sit high in their em
	if arrow {
		mark = st.script()
		raise = e.asc + s*0.05
	}
	mb := mark.glyphs(r, glyph, false, false)
	out.w = max(e.w, mb.w)
	out.draw = func(r renderer, x, base float64) {
		e.paint(r, x+(out.w-e.w)/2, base)
		mb.paint(r, x+(out.w-mb.w)/2, base-raise)
	}
	return out
}

// bar lays out m:bar: a line under (the default) or over the argument.
func (st mathStyle) bar(r renderer, n *xmlNode) mathBox {
	e := st.args(r, n, "e")
	s := st.size()
	top := false
	if v, _ := n.prop("barPr", "pos"); v == "top" {
		top = true
	}
	out := e
	if top {
		out.asc += s * 0.15
	} else {
		out.desc += s * 0.15
	}
	out.draw = func(r renderer, x, base float64) {
		e.paint(r, x, base)
		y := base + e.desc + s*0.08
		if top {
			y = base - e.asc - s*0.08
		}
		r.StrokeLine(x, y, x+e.w, y, st.rule(), st.ink())
	}
	return out
}

// borderBox lays out m:borderBox: the argument framed.
func (st mathStyle) borderBox(r renderer, n *xmlNode) mathBox {
	e := st.args(r, n, "e")
	pad := st.size() * 0.12
	out := mathBox{w: e.w + 2*pad, asc: e.asc + pad, desc: e.desc + pad}
	out.draw = func(r renderer, x, base float64) {
		e.paint(r, x+pad, base)
		ink, t := st.ink(), st.rule()
		x1, y0, y1 := x+out.w, base-out.asc, base+out.desc
		r.StrokeLine(x, y0, x1, y0, t, ink)
		r.StrokeLine(x, y1, x1, y1, t, ink)
		r.StrokeLine(x, y0, x, y1, t, ink)
		r.StrokeLine(x1, y0, x1, y1, t, ink)
	}
	return out
}

// groupChr lays out m:groupChr: a horizontal brace (drawn in strokes) or
// another character under or over the argument.
func (st mathStyle) groupChr(r renderer, n *xmlNode) mathBox {
	e := st.args(r, n, "e")
	s := st.size()
	chr := "⏟"
	if v, ok := n.prop("groupChrPr", "chr"); ok && v != "" {
		chr = v
	}
	top := chr == "⏞"
	if v, ok := n.prop("groupChrPr", "pos"); ok {
		top = v == "top"
	}
	if chr != "⏟" && chr != "⏞" {
		mark := st.glyphs(r, chr, false, false)
		w := max(e.w, mark.w)
		gap := s * 0.05
		out := mathBox{w: w, asc: e.asc, desc: e.desc}
		var shift float64 // mark's baseline below ours
		if top {
			shift = -(e.asc + gap + mark.desc)
			out.asc = -shift + mark.asc
		} else {
			shift = e.desc + gap + mark.asc
			out.desc = shift + mark.desc
		}
		out.draw = func(r renderer, x, base float64) {
			e.paint(r, x+(w-e.w)/2, base)
			mark.paint(r, x+(w-mark.w)/2, base+shift)
		}
		return out
	}
	d := s * 0.3
	out := e
	if top {
		out.asc += d + s*0.1
	} else {
		out.desc += d + s*0.1
	}
	out.draw = func(r renderer, x, base float64) {
		e.paint(r, x, base)
		y0, dir := base+e.desc+s*0.1, 1.0
		if top {
			y0, dir = base-e.asc-s*0.1, -1
		}
		x1, xm, k := x+e.w, x+e.w/2, min(s*0.15, e.w/6)
		pts := [][2]float64{{x, y0}, {x + k, y0 + dir*d/2}, {xm - k, y0 + dir*d/2}, {xm, y0 + dir*d}, {xm + k, y0 + dir*d/2}, {x1 - k, y0 + dir*d/2}, {x1, y0}}
		for i := 1; i < len(pts); i++ {
			r.StrokeLine(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1], st.rule(), st.ink())
		}
	}
	return out
}

// grid lays out rows of cells (a matrix, or an equation array's rows) in
// columns, each cell centered in its column, the whole centered on the axis.
func (st mathStyle) grid(rows [][]mathBox) mathBox {
	s := st.size()
	colGap, rowGap := s*0.8, s*0.25
	var colW []float64
	rowAsc, rowDesc := make([]float64, len(rows)), make([]float64, len(rows))
	for i, row := range rows {
		for j, c := range row {
			if j == len(colW) {
				colW = append(colW, 0)
			}
			colW[j] = max(colW[j], c.w)
			rowAsc[i] = max(rowAsc[i], c.asc, s*mathAscent)
			rowDesc[i] = max(rowDesc[i], c.desc, s*mathDescent)
		}
	}
	var w, h float64
	for j, cw := range colW {
		w += cw
		if j > 0 {
			w += colGap
		}
	}
	for i := range rows {
		h += rowAsc[i] + rowDesc[i]
		if i > 0 {
			h += rowGap
		}
	}
	axis := s * mathAxis
	return mathBox{
		w: w, asc: h/2 + axis, desc: h/2 - axis,
		draw: func(r renderer, x, base float64) {
			y := base - axis - h/2
			for i, row := range rows {
				rowBase := y + rowAsc[i]
				cx := x
				for j, c := range row {
					c.paint(r, cx+(colW[j]-c.w)/2, rowBase)
					cx += colW[j] + colGap
				}
				y = rowBase + rowDesc[i] + rowGap
			}
		},
	}
}
