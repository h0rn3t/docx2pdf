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
	"strconv"
)

// nbPagesAlias is the placeholder a NUMPAGES field draws; the fpdf backend
// (AliasNbPages) rewrites it to the final page count when the PDF is closed.
// The count's width is measured on the placeholder, so a "Page X of Y" line
// whose Y has a different digit count shifts by a few points - acceptable.
const nbPagesAlias = "{nb}"

// fillFields returns p with its computed field runs (PAGE/NUMPAGES) resolved to
// text: the current page number, or the total-pages placeholder. Paragraphs
// without a field run are returned unchanged; otherwise the runs are copied so
// the document model itself is not mutated (headers are re-rendered per page).
func fillFields(p Paragraph, pageNo int) Paragraph {
	has := false
	for _, r := range p.Runs {
		if r.Field != "" {
			has = true
			break
		}
	}
	if !has {
		return p
	}
	runs := make([]Run, len(p.Runs))
	copy(runs, p.Runs)
	for i := range runs {
		switch runs[i].Field {
		case FieldPage:
			runs[i].Text = strconv.Itoa(pageNo)
		case FieldNumPages:
			runs[i].Text = nbPagesAlias
		}
	}
	p.Runs = runs
	return p
}

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
	doc *Document
}

// ConvertToPdf returns a Converter that renders doc's body as flowed PDF text.
func ConvertToPdf(doc *Document) *Converter {
	return &Converter{doc: doc}
}

// Write renders the document and writes the complete PDF byte stream to w.
func (c *Converter) Write(w io.Writer) error {
	g := c.geometry()
	r := newFPDFRenderer(g.WidthPt, g.HeightPt)
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

// startingAt returns the same section with its columns starting at y instead of
// the top margin - what a continuous section break does: the new column layout
// resumes at the cursor on the current page.
func (sl sectionLayout) startingAt(y float64) sectionLayout {
	cols := make([]page, len(sl.cols))
	copy(cols, sl.cols)
	for i := range cols {
		cols[i].originY = y
	}
	return sectionLayout{sec: sl.sec, cols: cols}
}

// flow is the drawing cursor: which section and column content is flowing
// through, where in it, and how to move on when the frame is full. A fixed flow
// (header/footer content) never paginates - it draws where it is told and lets
// oversized content overflow, since Word's own header growth is out of scope.
type flow struct {
	r             renderer
	sec           sectionLayout
	col           int
	y             float64
	atTop         bool
	pageInSection int
	pageNo        int // document-absolute page number, for PAGE fields
	fixed         bool
}

func (f *flow) frame() page { return f.sec.cols[f.col] }

// newPage starts a page in the current section, draws its header and footer,
// and puts the cursor at the top of the first column.
func (f *flow) newPage() {
	if f.fixed {
		return
	}
	f.r.AddPage(f.sec.sec.Geometry.WidthPt, f.sec.sec.Geometry.HeightPt)
	// A continuous section started mid-page; every page after that one starts
	// its columns at the top margin again.
	f.sec = f.sec.startingAt(pageFrom(f.sec.sec.Geometry).originY)
	f.pageInSection++
	f.pageNo++
	drawHeaderFooter(f.r, f.sec, f.pageInSection, f.pageNo)
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
		f.col++
		f.y = f.frame().originY
		f.atTop = true
		return
	}
	f.newPage()
}

// startSection switches the flow to sec. A continuous section break keeps the
// current page and resumes its (possibly different) column layout at the
// cursor; every other break type starts a new page.
//
// ponytail: continuous columns are filled top to bottom, not balanced across
// the section's columns as Word does - balancing needs a second measuring pass
// over the whole section.
func (f *flow) startSection(sec sectionLayout) {
	if sec.sec.Continuous {
		f.sec = sec.startingAt(f.y)
		f.col = 0
		return
	}
	f.sec = sec
	f.pageInSection = 0
	f.newPage()
}

// render lays every body element out across pages and sections. It always emits
// at least one page so the output is a valid PDF even for an empty or nil
// document.
func (c *Converter) render(r renderer) {
	secs := c.sections()
	f := &flow{r: r, sec: secs[0]}
	f.newPage()

	if c.doc == nil {
		return
	}
	si := 0
	for i, el := range c.doc.Body {
		// A section ends *after* the paragraph carrying its w:sectPr, so the next
		// section takes over at the first element beyond its End.
		for si+1 < len(secs) && i >= secs[si].sec.End {
			si++
			f.startSection(secs[si])
		}
		renderElement(f, el)
	}
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
		renderTable(f, *el.Table)
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

	p = fillFields(p, f.pageNo)
	laidOutOn := f.pageNo
	lines := layoutParagraph(f.r, p, f.frame().contentWidth-p.Props.Indent.LeftPt-p.Props.Indent.RightPt)
	if len(lines) == 0 { // empty paragraph advances one line at its line spacing
		h := lineHeightFor(defaultRenderSizePt, 0, p.Props.Spacing)
		if f.y+h > f.frame().bottomLimit && !f.atTop {
			f.breakFrame()
		}
		f.y += h
		f.atTop = false
		f.y += p.Props.Spacing.AfterPt
		return
	}

	for i, ln := range lines {
		switch ln.breakBefore {
		case breakPage:
			f.newPage()
		case breakColumn:
			f.breakFrame()
		case breakNone:
		}
		if f.y+ln.height > f.frame().bottomLimit && !f.atTop {
			f.breakFrame()
		}
		if f.pageNo != laidOutOn {
			setPageNumber(f.r, &ln, f.pageNo)
		}
		fr := f.frame()
		innerX := fr.originX + p.Props.Indent.LeftPt
		innerWidth := fr.contentWidth - p.Props.Indent.LeftPt - p.Props.Indent.RightPt
		if p.Props.Shading != "" {
			// Painted per line, before the text: consecutive lines tile into one
			// unbroken block behind the paragraph, and a paragraph split across
			// columns or pages is shaded on each of them.
			f.r.FillRect(innerX, f.y, innerWidth, ln.height, p.Props.Shading)
		}
		drawLine(f.r, ln, p.Props.Alignment, innerX, innerWidth, f.y, i == len(lines)-1, p.Props.Indent.FirstLineOffsetPt, i == 0)
		f.y += ln.height
		f.atTop = false
	}
	f.y += p.Props.Spacing.AfterPt
}

// --- Headers and footers ---

// drawHeaderFooter draws the section's header and footer on the page that has
// just been added. On a title page (w:titlePg) the "first" parts replace the
// default ones, and on an even page of a document with w:evenAndOddHeaders the
// "even" parts do - when the section has none of that kind, the page simply
// gets no header/footer, which is exactly what Word draws.
func drawHeaderFooter(r renderer, sl sectionLayout, pageInSection, pageNo int) {
	s := sl.sec
	hdr, ftr := s.Header, s.Footer
	switch {
	case s.TitlePage && pageInSection == 1:
		hdr, ftr = s.FirstHeader, s.FirstFooter
	case s.EvenAndOddHeaders && pageNo%2 == 0:
		hdr, ftr = s.EvenHeader, s.EvenFooter
	}
	base := pageFrom(s.Geometry)
	if len(hdr) > 0 {
		drawBlock(r, hdr, base.originX, s.HeaderOffsetPt, base.contentWidth, pageNo)
	}
	if len(ftr) > 0 {
		// The footer sits above its distance from the bottom edge, so it is
		// measured first and drawn from there: a two-line footer grows upward,
		// as in Word, rather than off the page.
		h := blockHeight(r, ftr, base.contentWidth)
		drawBlock(r, ftr, base.originX, s.Geometry.HeightPt-s.FooterOffsetPt-h, base.contentWidth, pageNo)
	}
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

// drawBlock draws elements at a fixed origin, with no pagination of its own.
// pageNo is the page these fixed elements sit on, so a PAGE field in a header or
// footer shows that page's own number.
func drawBlock(r renderer, elems []BodyElement, x, y, width float64, pageNo int) {
	f := &flow{
		r:      r,
		fixed:  true,
		pageNo: pageNo,
		sec: sectionLayout{cols: []page{{
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
