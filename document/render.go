// Rendering of a Document into a paginated PDF: text measurement, word-wrap,
// per-run formatting, alignment, column and page breaks, headers and footers.
// The simplest entry points are the Document methods WritePDF (to a file) and
// WritePDFTo (to an io.Writer); ConvertToPdf exposes the same rendering as a
// reusable Converter.
package document

import (
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
)

// fillFields returns p with its computed field runs (PAGE/NUMPAGES) resolved to
// text: the current page number, or the document's page count. Paragraphs
// without a field run are returned unchanged; otherwise the runs are copied so
// the document model itself is not mutated (headers are re-rendered per page).
func fillFields(p Paragraph, pageNo, numPages int) Paragraph {
	if !slices.ContainsFunc(p.Runs, func(r Run) bool { return r.Field != "" }) {
		return p
	}
	runs := slices.Clone(p.Runs)
	for i := range runs {
		switch runs[i].Field {
		case FieldPage:
			runs[i].Text = strconv.Itoa(pageNo)
		case FieldNumPages:
			runs[i].Text = strconv.Itoa(numPages)
		}
	}
	p.Runs = runs
	return p
}

// fillTableFields is fillFields for every paragraph of t, nested tables
// included. A table drawn across pages shows the page it starts on.
func fillTableFields(t Table, pageNo, numPages int) Table {
	if !hasFields([]BodyElement{{Table: &t}}, "") {
		return t
	}
	t.Rows = slices.Clone(t.Rows)
	for ri := range t.Rows {
		cells := slices.Clone(t.Rows[ri].Cells)
		for ci := range cells {
			c := &cells[ci]
			c.Content = fillBlockFields(c.Content, pageNo, numPages)
			paras := make([]Paragraph, len(c.Paragraphs))
			for i, p := range c.Paragraphs {
				paras[i] = fillFields(p, pageNo, numPages)
			}
			c.Paragraphs = paras
			if c.Nested != nil {
				nt := fillTableFields(*c.Nested, pageNo, numPages)
				c.Nested = &nt
			}
		}
		t.Rows[ri].Cells = cells
	}
	return t
}

func fillBlockFields(elems []BodyElement, pageNo, numPages int) []BodyElement {
	out := make([]BodyElement, len(elems))
	for i, el := range elems {
		switch {
		case el.Paragraph != nil:
			p := fillFields(*el.Paragraph, pageNo, numPages)
			out[i].Paragraph = &p
		case el.Table != nil:
			t := fillTableFields(*el.Table, pageNo, numPages)
			out[i].Table = &t
		}
	}
	return out
}

// hasFields reports whether any paragraph in elems - inside tables too -
// carries a field run of the given kind, or of any kind for "".
func hasFields(elems []BodyElement, kind string) bool {
	for _, el := range elems {
		switch {
		case el.Paragraph != nil:
			if slices.ContainsFunc(el.Paragraph.Runs, func(r Run) bool { return r.Field != "" && (kind == "" || r.Field == kind) }) {
				return true
			}
		case el.Table != nil:
			for _, row := range el.Table.Rows {
				for _, c := range row.Cells {
					if hasFields(c.blocks(), kind) {
						return true
					}
				}
			}
		}
	}
	return false
}

// usesNumPages reports whether the document shows its page count anywhere -
// body, headers or footers - so rendering must learn it first.
func (d *Document) usesNumPages() bool {
	if d == nil {
		return false
	}
	if hasFields(d.Body, FieldNumPages) {
		return true
	}
	for _, s := range d.Sections {
		for _, part := range [][]BodyElement{s.Header, s.Footer, s.FirstHeader, s.FirstFooter, s.EvenHeader, s.EvenFooter} {
			if hasFields(part, FieldNumPages) {
				return true
			}
		}
	}
	return false
}

// dryRenderer draws nothing: it measures text through the wrapped renderer and
// counts pages, so a layout pass can be run just to learn where things land.
type dryRenderer struct {
	renderer
	pages int
}

func (d *dryRenderer) AddPage(float64, float64)                                       { d.pages++ }
func (d *dryRenderer) DrawText(float64, float64, string)                              {}
func (d *dryRenderer) DrawImage(float64, float64, float64, float64, *Image)           {}
func (d *dryRenderer) FillRect(float64, float64, float64, float64, string)            {}
func (d *dryRenderer) StrokeLine(float64, float64, float64, float64, float64, string) {}
func (d *dryRenderer) Rotate(float64, float64, float64)                               {}
func (d *dryRenderer) RotateEnd()                                                     {}
func (d *dryRenderer) Clip(float64, float64, float64, float64)                        {}
func (d *dryRenderer) ClipEnd()                                                       {}
func (d *dryRenderer) Link(float64, float64, float64, float64, string)                {}
func (d *dryRenderer) Anchor(string, float64)                                         {}
func (d *dryRenderer) Output(io.Writer) error                                         { return nil }

// setPageNumber rewrites ln's PAGE field words to pageNo and re-measures them:
// a paragraph that runs onto another page was laid out with the page it started
// on, and a number with more digits must push what follows it along.
func setPageNumber(r renderer, ln *line, pageNo int) {
	for i := range ln.words {
		w := &ln.words[i]
		if w.field != FieldPage {
			continue
		}
		w.text = strconv.Itoa(pageNo)
		r.SetFont(w.props.FontFamily, w.props.Bold, w.props.Italic, w.props.Underline, w.props.SizePt)
		width := r.TextWidth(w.text)
		ln.natural += width - w.width
		w.width = width
	}
}

// WritePDF renders the document and writes the PDF to path, in one call:
//
//	doc, err := document.Open("in.docx")
//	...
//	err = doc.WritePDF("out.pdf")
func (d *Document) WritePDF(path string) error {
	return ConvertToPdf(d).WriteToFile(path)
}

// WritePDFTo renders the document and writes the complete PDF byte stream to w.
func (d *Document) WritePDFTo(w io.Writer) error {
	return ConvertToPdf(d).Write(w)
}

// Converter renders a document to PDF. Create one with ConvertToPdf.
type Converter struct {
	doc    *Document
	system *fontIndex
}

// WithSystemFonts makes the converter draw text in the fonts installed on this
// machine whenever the document asks for one - as Word does - instead of the
// bundled metric-compatible replacements; fonts the document embeds are used
// either way. dirs are where to look; with none, the platform's usual font
// directories (the user's own included) are searched. Only TrueType files are
// used, and a string a font has no glyph for is drawn in the replacement. The
// output then depends on the machine it is made on.
func (c *Converter) WithSystemFonts(dirs ...string) *Converter {
	c.system = newFontIndex(dirs)
	return c
}

// ConvertToPdf returns a Converter that renders doc's body as flowed PDF text.
func ConvertToPdf(doc *Document) *Converter {
	return &Converter{doc: doc}
}

// Write renders the document and writes the complete PDF byte stream to w.
func (c *Converter) Write(w io.Writer) error {
	g := c.geometry()
	var embedded []Font
	if c.doc != nil {
		embedded = c.doc.Fonts
	}
	r := newFPDFRenderer(g.WidthPt, g.HeightPt, embedded, c.system)
	c.render(r)
	return r.Output(w)
}

// geometry returns the page geometry of the document's first section (from
// w:sectPr), or the A4/one-inch default - also used for a nil document or one
// built without geometry (a non-positive width).
func (c *Converter) geometry() PageGeometry {
	if c.doc == nil || c.doc.Geometry.WidthPt <= 0 {
		return DefaultPageGeometry()
	}
	return c.doc.Geometry
}

// WriteToFile renders the document and writes the PDF to path. It returns a
// non-nil error if the destination cannot be created or written; it never
// panics.
func (c *Converter) WriteToFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("docx2pdf: create pdf %q: %w", path, err)
	}
	if err := c.Write(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("docx2pdf: write pdf %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("docx2pdf: close pdf %q: %w", path, err)
	}
	return nil
}

// sectionLayout is a Section resolved into the frames content flows through:
// one page frame per column, left to right.
type sectionLayout struct {
	sec  Section
	cols []page
}

// layoutSection places a section's columns across its content width. A section
// with no usable column list gets one full-width column, so there is always a
// frame to flow into.
func layoutSection(s Section) sectionLayout {
	base := pageFrom(s.Geometry)
	cols := make([]page, 0, len(s.Columns))
	x := base.originX
	for _, c := range s.Columns {
		if c.WidthPt <= 0 {
			continue
		}
		cols = append(cols, page{originX: x, originY: base.originY, contentWidth: c.WidthPt, bottomLimit: base.bottomLimit})
		x += c.WidthPt + c.SpaceAfterPt
	}
	if len(cols) == 0 {
		cols = []page{base}
	}
	return sectionLayout{sec: s, cols: cols}
}

// framed returns the same section with its columns running from top to bottom
// instead of margin to margin: a continuous section break resumes the new
// column layout at the cursor on the current page, and a tall header or footer
// pushes the body away from it.
func (sl sectionLayout) framed(top, bottom float64) sectionLayout {
	cols := slices.Clone(sl.cols)
	for i := range cols {
		cols[i].originY, cols[i].bottomLimit = top, bottom
	}
	return sectionLayout{sec: sl.sec, cols: cols}
}

// flow is the drawing cursor: which section and column content is flowing
// through, where in it, and how to move on when the frame is full. A fixed flow
// (header/footer content) never paginates - it draws where it is told; a tall
// header or footer moves the body's frame instead (see newPage).
type flow struct {
	r             renderer
	sec           sectionLayout
	col           int
	y             float64
	atTop         bool
	pageInSection int
	pageNo        int // document-absolute page number, for PAGE fields
	numPages      int // the document's page count, for NUMPAGES fields
	fixed         bool
	// top is where the body starts on the current page: the top margin, or
	// below a header too tall for it.
	top float64
	// floats are the areas floating objects keep the text out of on the
	// current page.
	floats []floatBox
	// bottom is where the body ends on the current page: the bottom margin, or
	// above a footer too tall for it.
	bottom float64
	// lowest is how far down the columns already left on this page reached.
	lowest float64
	// notes are the footnotes referenced on the current page, drawn at its
	// foot when the page is done; the body's frames end above them.
	notes []*Note
}

func (f *flow) frame() page { return f.sec.cols[f.col] }

// newPage starts a page in the current section, draws its header and footer,
// and puts the cursor at the top of the first column.
func (f *flow) newPage() {
	if f.fixed {
		return
	}
	f.drawNotes()
	f.r.AddPage(f.sec.sec.Geometry.WidthPt, f.sec.sec.Geometry.HeightPt)
	f.pageInSection++
	f.pageNo++
	f.floats = nil
	// A continuous section started mid-page; every page after that one starts
	// its columns at the top margin again - or below a header that reaches
	// past it, and ends them above a footer that does.
	base := pageFrom(f.sec.sec.Geometry)
	hdrBottom, ftrTop := drawHeaderFooter(f.r, f.sec, f.pageInSection, f.pageNo, f.numPages)
	f.top, f.bottom, f.lowest = max(base.originY, hdrBottom), min(base.bottomLimit, ftrTop), 0
	f.sec = f.sec.framed(f.top, f.bottom)
	drawColumnSeparators(f.r, f.sec)
	f.col = 0
	f.y = f.frame().originY
	f.atTop = true
}

// breakFrame moves the cursor to the next column, or - past the last one - to a
// new page.
func (f *flow) breakFrame() {
	if f.fixed {
		return
	}
	if f.col+1 < len(f.sec.cols) {
		f.lowest = max(f.lowest, f.y)
		f.col++
		f.y = f.frame().originY
		f.atTop = true
		return
	}
	f.newPage()
}

// startSection switches the flow to sec. A continuous section break keeps the
// current page and resumes its (possibly different) column layout at the
// cursor; every other break type starts a new page. (The columns of the
// section a continuous break ends were balanced by balanceSection.)
func (f *flow) startSection(sec sectionLayout) {
	if sec.sec.Continuous {
		// The new layout starts below the longest column of the last one, and
		// this page's footer still applies.
		f.y = max(f.y, f.lowest)
		f.sec = sec.framed(f.y, f.bottom)
		f.col, f.lowest = 0, 0
		return
	}
	f.drawNotes() // at the foot of the page they were placed on, in its geometry
	f.sec = sec
	f.pageInSection = 0
	f.newPage()
}

// maxPageCountPasses bounds the dry runs that settle a NUMPAGES value: the
// count's own digits can move a line, and in theory a page, so it is re-run
// until it stops changing - which in practice takes one or two passes.
const maxPageCountPasses = 3

// render lays every body element out across pages and sections. It always emits
// at least one page so the output is a valid PDF even for an empty or nil
// document. A document that shows its page count (NUMPAGES) is first laid out
// without drawing to learn that count.
func (c *Converter) render(r renderer) {
	numPages := 0
	if c.doc.usesNumPages() {
		for range maxPageCountPasses {
			dry := &dryRenderer{renderer: r}
			c.renderPass(dry, numPages)
			if dry.pages == numPages {
				break
			}
			numPages = dry.pages
		}
	}
	c.renderPass(r, numPages)
}

// renderPass is one layout of the whole document into r, with NUMPAGES fields
// showing numPages.
func (c *Converter) renderPass(r renderer, numPages int) {
	secs := c.sections()
	f := &flow{r: r, sec: secs[0], numPages: numPages}
	f.newPage()

	if c.doc == nil {
		return
	}
	si := 0
	balanceSection(f, secs, si, c.doc.Body)
	for i, el := range c.doc.Body {
		// A section ends *after* the paragraph carrying its w:sectPr, so the next
		// section takes over at the first element beyond its End.
		for si+1 < len(secs) && i >= secs[si].sec.End {
			si++
			f.startSection(secs[si])
			balanceSection(f, secs, si, c.doc.Body)
		}
		renderElement(f, el)
	}
	if len(c.doc.Endnotes) > 0 {
		f.y += noteSeparatorPt
		drawNoteSeparator(f.r, f.frame().originX, f.y-noteSeparatorPt/2)
		for _, el := range c.doc.Endnotes {
			renderElement(f, el)
		}
	}
	f.drawNotes()
}

// noteSeparatorPt is the room the rule above the notes takes, and
// noteSeparatorWidthPt how long that rule is: Word's two inches.
const (
	noteSeparatorPt      = 12.0
	noteSeparatorWidthPt = 144.0
)

func drawNoteSeparator(r renderer, x, y float64) {
	r.StrokeLine(x, y, x+noteSeparatorWidthPt, y, columnSeparatorWidthPt, "#000000")
}

// lineNotes lists the footnotes the words reference.
func lineNotes(words []word) []*Note {
	var notes []*Note
	for _, w := range words {
		if w.note != nil && !w.note.Endnote {
			notes = append(notes, w.note)
		}
	}
	return notes
}

// notesHeight is how much room notes take at the foot of a page, the rule
// above them included when first is set.
func (f *flow) notesHeight(notes []*Note, first bool) float64 {
	if len(notes) == 0 {
		return 0
	}
	var h float64
	if first {
		h = noteSeparatorPt
	}
	width := pageFrom(f.sec.sec.Geometry).contentWidth
	for _, n := range notes {
		h += blockHeight(f.r, n.Body, width)
	}
	return h
}

// notesFit reports whether a block h high at the cursor still leaves room
// below it for the notes it references. At the top of a frame there is
// nowhere better to go, so it always fits.
func (f *flow) notesFit(notes []*Note, h float64) bool {
	if f.fixed || len(notes) == 0 || f.atTop {
		return true
	}
	return f.y+h <= f.frame().bottomLimit-f.notesHeight(notes, len(f.notes) == 0)
}

// addNotes reserves room for notes at the foot of the page: the body's frames
// end that much higher.
func (f *flow) addNotes(notes []*Note) {
	if f.fixed || len(notes) == 0 {
		return
	}
	h := f.notesHeight(notes, len(f.notes) == 0)
	f.notes = append(f.notes, notes...)
	f.bottom -= h
	f.sec.cols = slices.Clone(f.sec.cols)
	for i := range f.sec.cols {
		f.sec.cols[i].bottomLimit -= h
	}
}

// drawNotes draws the page's footnotes in the room reserved for them, under a
// short rule, and clears them.
func (f *flow) drawNotes() {
	if len(f.notes) == 0 {
		return
	}
	base := pageFrom(f.sec.sec.Geometry)
	drawNoteSeparator(f.r, base.originX, f.bottom+noteSeparatorPt/2)
	y := f.bottom + noteSeparatorPt
	for _, n := range f.notes {
		y = drawBlock(f.r, f.sec.sec, n.Body, base.originX, y, base.contentWidth, f.pageNo, f.numPages)
	}
	f.notes = nil
}

// balanceSection evens out the columns of section si when a continuous break
// ends it: Word then fills them to an equal height rather than top to bottom,
// so the section after it starts right below. It tries the section's elements
// in dry runs to find the lowest column bottom at which they still fit on the
// current page, and ends the columns there. A section that runs onto another
// page anyway keeps its full columns.
func balanceSection(f *flow, secs []sectionLayout, si int, body []BodyElement) {
	if si+1 >= len(secs) || !secs[si+1].sec.Continuous || len(f.sec.cols) < 2 {
		return
	}
	from := 0
	if si > 0 {
		from = min(secs[si-1].sec.End, len(body))
	}
	elems := body[from:max(from, min(secs[si].sec.End, len(body)))]
	top, bottom := f.frame().originY, f.frame().bottomLimit
	fits := func(limit float64) bool {
		sim := *f
		sim.r = &dryRenderer{renderer: f.r}
		sim.floats = slices.Clone(f.floats)
		sim.notes = slices.Clone(f.notes)
		sim.sec = f.sec.framed(top, limit)
		for _, el := range elems {
			renderElement(&sim, el)
		}
		return sim.pageNo == f.pageNo
	}
	if !fits(bottom) {
		return
	}
	const precisionPt = 0.5
	lo, hi := top, bottom
	for hi-lo > precisionPt {
		if mid := (lo + hi) / 2; fits(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	f.sec = f.sec.framed(top, hi)
}

// sections returns the document's sections as drawable layouts, synthesizing a
// single full-width one for a nil document (or a Document built without
// sections, e.g. in tests).
func (c *Converter) sections() []sectionLayout {
	if c.doc == nil || len(c.doc.Sections) == 0 {
		g := c.geometry()
		return []sectionLayout{layoutSection(Section{
			Geometry: g,
			Columns:  []Column{{WidthPt: g.WidthPt - g.MarginLeftPt - g.MarginRightPt}},
			End:      math.MaxInt,
		})}
	}
	out := make([]sectionLayout, len(c.doc.Sections))
	for i, s := range c.doc.Sections {
		out[i] = layoutSection(s)
	}
	return out
}

func renderElement(f *flow, el BodyElement) {
	switch {
	case el.Paragraph != nil:
		renderParagraph(f, *el.Paragraph)
	case el.Table != nil:
		renderTable(f, fillTableFields(*el.Table, f.pageNo, f.numPages))
	}
}

// renderParagraph lays out and draws one paragraph across the current column
// (narrowed by the paragraph's own indent), moving to the next column or page as
// needed. The paragraph's own space-before/space-after is added as a plain
// cursor advance with no overflow check of its own - a table's surrounding
// vertical gap is produced entirely by its neighboring paragraphs' spacing this
// way, with no changes needed in table.go.
func renderParagraph(f *flow, p Paragraph) {
	switch {
	case p.Props.PageBreak && !f.atTop:
		f.newPage()
	case p.Props.ColumnBreak && !f.atTop:
		f.breakFrame()
	}
	f.y += p.Props.Spacing.BeforePt

	p = fillFields(p, f.pageNo, f.numPages)
	p, floats := splitFloats(p)
	for _, img := range floats {
		f.placeFloat(img)
	}
	laidOutOn := f.pageNo
	ind := p.Props.Indent
	b := newLineBreaker(f.r, p)
	if b.done() { // empty paragraph advances one line at its line spacing
		h := lineHeightFor(defaultRenderSizePt, 0, p.Props.Spacing)
		f.lineSlot(ind, h)
		f.placeAnchors(p.Bookmarks)
		f.y += h
		f.atTop = false
		f.y += p.Props.Spacing.AfterPt
		return
	}

	for first := true; !b.done(); first = false {
		retry := b
		width := f.frame().contentWidth - ind.LeftPt - ind.RightPt
		ln, _ := b.next(width)
		switch ln.breakBefore {
		case breakPage:
			f.newPage()
		case breakColumn:
			f.breakFrame()
		case breakNone:
		}
		// The line is packed for the frame's width; when it lands in another
		// frame, or beside a floating object, it is packed again for the room
		// it has there.
		x, slot := f.lineSlot(ind, ln.height)
		if slot != width {
			b = retry
			ln, _ = b.next(slot)
		}
		// A line referencing footnotes goes where they fit below it, on the
		// same page.
		notes := lineNotes(ln.words)
		for !f.notesFit(notes, ln.height) {
			f.breakFrame()
			x, slot = f.lineSlot(ind, ln.height)
		}
		if f.pageNo != laidOutOn {
			setPageNumber(f.r, &ln, f.pageNo)
		}
		if first {
			f.placeAnchors(p.Bookmarks)
		}
		if p.Props.Shading != "" {
			// Painted per line, before the text: consecutive lines tile into one
			// unbroken block behind the paragraph, and a paragraph split across
			// columns or pages is shaded on each of them.
			f.r.FillRect(x, f.y, slot, ln.height, p.Props.Shading)
		}
		drawBarTabs(f.r, p.Props.Tabs, f.frame().originX, f.y, ln.height)
		drawLine(f.r, ln, p.Props.Alignment, x, slot, f.y, b.done(), ind.FirstLineOffsetPt, first)
		f.y += ln.height
		f.atTop = false
		f.addNotes(notes)
	}
	f.y += p.Props.Spacing.AfterPt
}

// minWrapWidthPt is the narrowest room beside a floating object that a line
// is set in; with less, the line moves below the object instead.
const minWrapWidthPt = 36.0

// floatBox is the area a floating object keeps the text out of on the current
// page: its own rectangle grown by its clearance.
type floatBox struct {
	x0, y0, x1, y1 float64
	wrap           Wrap
}

// splitFloats takes the floating pictures out of p's runs: they are drawn
// where their anchor puts them, not in the line.
func splitFloats(p Paragraph) (Paragraph, []*Image) {
	if !slices.ContainsFunc(p.Runs, func(r Run) bool { return r.Image != nil && r.Image.Float != nil }) {
		return p, nil
	}
	var floats []*Image
	runs := make([]Run, 0, len(p.Runs))
	for _, r := range p.Runs {
		if r.Image != nil && r.Image.Float != nil {
			floats = append(floats, r.Image)
			continue
		}
		runs = append(runs, r)
	}
	p.Runs = runs
	return p, floats
}

// placeFloat draws a floating picture where its anchor puts it - measured, for
// a paragraph anchor, from the cursor at the paragraph's top - and keeps the
// text out of its area from then on.
func (f *flow) placeFloat(img *Image) {
	x, y := floatOrigin(f, *img.Float, img.WidthPt, img.HeightPt)
	drawPicture(f.r, img, x, y)
	f.addFloat(*img.Float, x, y, img.WidthPt, img.HeightPt)
}

// addFloat records the area of a floating object drawn at (x, y), w × h, for
// the text on the rest of the page to wrap around; text may run over an object
// that does not wrap.
func (f *flow) addFloat(fl Float, x, y, w, h float64) {
	if fl.Wrap == WrapNone {
		return
	}
	f.floats = append(f.floats, floatBox{
		x0: x - fl.DistLeftPt, y0: y - fl.DistTopPt,
		x1: x + w + fl.DistRightPt, y1: y + h + fl.DistBottomPt,
		wrap: fl.Wrap,
	})
}

// lineSlot finds where the next line of a paragraph indented by ind, h high,
// goes. It moves the cursor on - to the next frame when the line does not fit,
// below any floating object that leaves it too little room - and returns the
// left edge and width the line has there: the indented text area, narrowed to
// the wider side of any floating object beside it.
func (f *flow) lineSlot(ind Indent, h float64) (x, width float64) {
	for {
		if f.y+h > f.frame().bottomLimit && !f.atTop {
			f.breakFrame()
		}
		fr := f.frame()
		lo, hi, below := avoidFloats(f.floats, fr.originX+ind.LeftPt, fr.originX+fr.contentWidth-ind.RightPt, f.y, f.y+h)
		if below <= f.y {
			return lo, hi - lo
		}
		f.y, f.atTop = below, false
	}
}

// avoidFloats narrows [lo, hi] to the wider side of each floating object in
// the band [top, bottom]. It returns the narrowed span, and - when that is too
// narrow to set a line in, or an object wants the band to itself - the y below
// the objects in the way, where the line should go instead; otherwise top.
func avoidFloats(floats []floatBox, lo, hi, top, bottom float64) (float64, float64, float64) {
	below := top
	for _, fb := range floats {
		if fb.y1 <= top || fb.y0 >= bottom || fb.x1 <= lo || fb.x0 >= hi {
			continue
		}
		below = max(below, fb.y1)
		switch {
		case fb.wrap == WrapTopAndBottom:
			hi = lo
		case fb.x0-lo >= hi-fb.x1:
			hi = fb.x0
		default:
			lo = fb.x1
		}
	}
	if hi-lo < minWrapWidthPt {
		return lo, hi, below
	}
	return lo, hi, top
}

// skipFloats moves the cursor below every floating object in the band an
// h-high block spanning [lo, hi] would take: unlike a line, a table row cannot
// narrow around one.
func (f *flow) skipFloats(lo, hi, h float64) {
	for moved := true; moved; {
		moved = false
		for _, fb := range f.floats {
			if fb.y1 > f.y && fb.y0 < f.y+h && fb.x1 > lo && fb.x0 < hi {
				f.y, f.atTop, moved = fb.y1, false, true
			}
		}
	}
}

// placeAnchors marks the cursor as the destination of the paragraph's
// bookmarks. A header or footer is drawn on every page, so its bookmarks would
// move to the last one; Word links to the body's, so fixed flows mark none.
func (f *flow) placeAnchors(names []string) {
	if f.fixed {
		return
	}
	for _, name := range names {
		f.r.Anchor(name, f.y)
	}
}

// --- Headers and footers ---

// drawHeaderFooter draws the section's header and footer on the page that has
// just been added, and returns how far down the header reaches and where the
// footer begins, so the body can keep clear of both. On a title page
// (w:titlePg) the "first" parts replace the default ones, and on an even page
// of a document with w:evenAndOddHeaders the "even" parts do - when the section
// has none of that kind, the page simply gets no header/footer, which is
// exactly what Word draws.
func drawHeaderFooter(r renderer, sl sectionLayout, pageInSection, pageNo, numPages int) (hdrBottom, ftrTop float64) {
	s := sl.sec
	hdr, ftr := s.Header, s.Footer
	switch {
	case s.TitlePage && pageInSection == 1:
		hdr, ftr = s.FirstHeader, s.FirstFooter
	case s.EvenAndOddHeaders && pageNo%2 == 0:
		hdr, ftr = s.EvenHeader, s.EvenFooter
	}
	base := pageFrom(s.Geometry)
	ftrTop = s.Geometry.HeightPt
	if len(hdr) > 0 {
		hdrBottom = drawBlock(r, s, hdr, base.originX, s.HeaderOffsetPt, base.contentWidth, pageNo, numPages)
	}
	if len(ftr) > 0 {
		// The footer sits above its distance from the bottom edge, so it is
		// measured first and drawn from there: a two-line footer grows upward,
		// as in Word, rather than off the page.
		ftrTop = s.Geometry.HeightPt - s.FooterOffsetPt - blockHeight(r, ftr, base.contentWidth)
		drawBlock(r, s, ftr, base.originX, ftrTop, base.contentWidth, pageNo, numPages)
	}
	return hdrBottom, ftrTop
}

// columnSeparatorWidthPt is the thickness of the w:sep rule between columns;
// Word draws a hairline, so this stays thin.
const columnSeparatorWidthPt = 0.5

// drawColumnSeparators draws the w:cols/@w:sep rule down the middle of each gap
// between adjacent columns, full column height, on the page just added. A no-op
// unless the section asked for separators and has more than one column.
func drawColumnSeparators(r renderer, sl sectionLayout) {
	if !sl.sec.Separator || len(sl.cols) < 2 {
		return
	}
	for i := 0; i+1 < len(sl.cols); i++ {
		left, right := sl.cols[i], sl.cols[i+1]
		gapStart := left.originX + left.contentWidth
		x := gapStart + (right.originX-gapStart)/2
		r.StrokeLine(x, left.originY, x, left.bottomLimit, columnSeparatorWidthPt, "#000000")
	}
}

// drawBlock draws elements at a fixed origin, with no pagination of its own,
// and returns the y it ended at. pageNo is the page these fixed elements sit
// on, so a PAGE field in a header or footer shows that page's own number; sec
// is the section whose page they are on, which a floating picture among them
// is positioned against.
func drawBlock(r renderer, sec Section, elems []BodyElement, x, y, width float64, pageNo, numPages int) float64 {
	f := &flow{
		r:        r,
		fixed:    true,
		pageNo:   pageNo,
		numPages: numPages,
		sec: sectionLayout{sec: sec, cols: []page{{
			originX:      x,
			originY:      y,
			contentWidth: width,
			bottomLimit:  math.Inf(1),
		}}},
		y:     y,
		atTop: true,
	}
	for _, el := range elems {
		renderElement(f, el)
	}
	return f.y
}

// blockHeight measures what drawBlock would draw, without drawing it: how tall
// the header/footer content is at the given width.
func blockHeight(r renderer, elems []BodyElement, width float64) float64 {
	var h float64
	for _, el := range elems {
		switch {
		case el.Paragraph != nil:
			p := *el.Paragraph
			h += p.Props.Spacing.BeforePt + p.Props.Spacing.AfterPt
			lines := layoutParagraph(r, p, width-p.Props.Indent.LeftPt-p.Props.Indent.RightPt)
			if len(lines) == 0 {
				h += lineHeightFor(defaultRenderSizePt, 0, p.Props.Spacing)
				continue
			}
			for _, ln := range lines {
				h += ln.height
			}
		case el.Table != nil:
			h += layoutTable(r, *el.Table, width, math.Inf(1)).totalHeight
		}
	}
	return h
}
