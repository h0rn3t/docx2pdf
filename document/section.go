package document

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// --- Sections (w:sectPr): page geometry, columns, headers and footers ---
//
// A document is a sequence of sections. Every section but the last ends at the
// paragraph whose w:pPr carries a w:sectPr; the last section's properties are
// the body-level w:sectPr. Each section has its own page size/margins, column
// layout, and header/footer parts.

// xmlCols is <w:cols>: the section's column layout. Columns are equal-width by
// default (w:num of them, separated by w:space); an explicit list of w:col
// children overrides that with per-column widths and gaps.
type xmlCols struct {
	Num        string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main num,attr"`
	Space      string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main space,attr"`
	EqualWidth string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main equalWidth,attr"`
	Sep        string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main sep,attr"`
	Cols       []xmlCol `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main col"`
}

// xmlCol is one <w:col>: its width and the gap to the next column, in dxa.
type xmlCol struct {
	W     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main w,attr"`
	Space string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main space,attr"`
}

// xmlHdrFtrRef is a <w:headerReference>/<w:footerReference>: which part
// (r:id) serves which page class (w:type = default/first/even).
type xmlHdrFtrRef struct {
	Type string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main type,attr"`
	ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
}

// maxColumns is Word's own limit on a section's columns; w:num is clamped to it
// so an absurd count in an untrusted file cannot allocate without bound.
const maxColumns = 45

// defaultColumnSpacePt is the gap Word puts between columns when w:cols
// declares none (720 dxa = 0.5").
const defaultColumnSpacePt = 36.0

// Default header/footer offsets from the page edge (720 dxa = 0.5"), used when
// w:pgMar declares no w:header/w:footer.
const defaultHdrFtrOffsetPt = 36.0

// Column is one column of a Section: its width and the gap that separates it
// from the next column (zero on the last one).
type Column struct {
	WidthPt, SpaceAfterPt float64
}

// Section is one w:sectPr's worth of a document: the page geometry, column
// layout, and header/footer content that apply to the body elements it covers.
type Section struct {
	Geometry PageGeometry
	// Columns always holds at least one column; a single full-width column is
	// the default when the section declares no w:cols.
	Columns []Column
	// Separator draws a vertical rule down the middle of each inter-column gap
	// (w:cols/@w:sep); only meaningful with more than one column.
	Separator bool
	// End is the exclusive index into Document.Body where this section ends -
	// Body[Start:End] with Start being the previous section's End.
	End int
	// Continuous reports a w:type="continuous" section break: the new section
	// resumes on the current page instead of starting a new one.
	Continuous bool
	// Header/Footer are the default page's content; FirstHeader/FirstFooter
	// replace them on the section's first page when TitlePage is set (an empty
	// slice then means "no header on the title page", which is what Word draws),
	// and EvenHeader/EvenFooter on even-numbered pages when EvenAndOddHeaders is
	// set. A section that references no part of a kind inherits the previous
	// section's.
	Header, Footer           []BodyElement
	FirstHeader, FirstFooter []BodyElement
	EvenHeader, EvenFooter   []BodyElement
	TitlePage                bool
	// EvenAndOddHeaders is the document-wide w:evenAndOddHeaders setting
	// (word/settings.xml), copied onto every section.
	EvenAndOddHeaders bool
	// HeaderOffsetPt/FooterOffsetPt are w:pgMar's w:header/w:footer: the header's
	// distance from the top edge of the page, and the footer's from the bottom.
	HeaderOffsetPt, FooterOffsetPt float64
}

// xmlSettings is word/settings.xml; only the even/odd header switch is modeled.
type xmlSettings struct {
	EvenAndOddHeaders *xmlOnOff `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main evenAndOddHeaders"`
}

// sectionBreak records a mid-body w:sectPr: the section's properties and the
// body index (exclusive) its paragraph sits at.
type sectionBreak struct {
	props *xmlSectPr
	end   int
}

// buildSections turns the mid-body section breaks plus the body-level w:sectPr
// (the last section's properties) into the document's sections. A document with
// neither still gets one default section covering the whole body, so the
// renderer always has a section to draw in. Each header/footer kind a section
// does not reference is inherited from the section before it.
func buildSections(breaks []sectionBreak, last *xmlSectPr, bodyLen int, evenAndOdd bool, p *pkg, sc styleContext) []Section {
	sects := make([]Section, 0, len(breaks)+1)
	for _, b := range breaks {
		sects = append(sects, buildSection(b.props, b.end, p, sc))
	}
	sects = append(sects, buildSection(last, bodyLen, p, sc))
	for i := range sects {
		sects[i].EvenAndOddHeaders = evenAndOdd
	}
	for i := 1; i < len(sects); i++ {
		sp := last
		if i < len(breaks) {
			sp = breaks[i].props
		}
		var hdrs, ftrs []xmlHdrFtrRef
		if sp != nil {
			hdrs, ftrs = sp.HeaderRefs, sp.FooterRefs
		}
		s, prev := &sects[i], sects[i-1]
		if !hasRef(hdrs, "default") {
			s.Header = prev.Header
		}
		if !hasRef(hdrs, "first") {
			s.FirstHeader = prev.FirstHeader
		}
		if !hasRef(hdrs, "even") {
			s.EvenHeader = prev.EvenHeader
		}
		if !hasRef(ftrs, "default") {
			s.Footer = prev.Footer
		}
		if !hasRef(ftrs, "first") {
			s.FirstFooter = prev.FirstFooter
		}
		if !hasRef(ftrs, "even") {
			s.EvenFooter = prev.EvenFooter
		}
	}
	return sects
}

// hasRef reports whether refs names a part for the given page kind.
func hasRef(refs []xmlHdrFtrRef, kind string) bool {
	for _, ref := range refs {
		if strings.EqualFold(strings.TrimSpace(ref.Type), kind) {
			return true
		}
	}
	return false
}

func buildSection(sp *xmlSectPr, end int, p *pkg, sc styleContext) Section {
	geom := buildPageGeometry(sp)
	s := Section{
		Geometry:       geom,
		Columns:        buildColumns(colsOf(sp), geom.WidthPt-geom.MarginLeftPt-geom.MarginRightPt),
		End:            end,
		HeaderOffsetPt: defaultHdrFtrOffsetPt,
		FooterOffsetPt: defaultHdrFtrOffsetPt,
	}
	if sp == nil {
		return s
	}
	if c := colsOf(sp); c != nil {
		switch strings.ToLower(strings.TrimSpace(c.Sep)) {
		case "1", "true", "on":
			s.Separator = true
		}
	}
	s.Continuous = sp.Type != nil && strings.EqualFold(strings.TrimSpace(sp.Type.Val), "continuous")
	s.TitlePage = sp.TitlePg.on()
	if sp.PgMar != nil {
		s.HeaderOffsetPt = dxaToPtOr(sp.PgMar.Header, defaultHdrFtrOffsetPt)
		s.FooterOffsetPt = dxaToPtOr(sp.PgMar.Footer, defaultHdrFtrOffsetPt)
	}
	s.Header = headerFooter(sp.HeaderRefs, "default", p, sc)
	s.FirstHeader = headerFooter(sp.HeaderRefs, "first", p, sc)
	s.EvenHeader = headerFooter(sp.HeaderRefs, "even", p, sc)
	s.Footer = headerFooter(sp.FooterRefs, "default", p, sc)
	s.FirstFooter = headerFooter(sp.FooterRefs, "first", p, sc)
	s.EvenFooter = headerFooter(sp.FooterRefs, "even", p, sc)
	return s
}

func colsOf(sp *xmlSectPr) *xmlCols {
	if sp == nil {
		return nil
	}
	return sp.Cols
}

// buildColumns resolves w:cols against the section's content width. Explicit
// w:col children win; otherwise w:num equal-width columns share the width,
// separated by w:space. An absent, single-column, or unusable w:cols yields one
// full-width column, so the caller can always flow into Columns[0].
func buildColumns(c *xmlCols, contentWidth float64) []Column {
	full := []Column{{WidthPt: contentWidth}}
	if c == nil {
		return full
	}
	if len(c.Cols) > 0 {
		cols := make([]Column, 0, len(c.Cols))
		for _, col := range c.Cols {
			w := dxaToPt(col.W)
			if w <= 0 {
				return full // an unusable explicit list: fall back to one column
			}
			cols = append(cols, Column{WidthPt: w, SpaceAfterPt: dxaToPt(col.Space)})
		}
		cols[len(cols)-1].SpaceAfterPt = 0
		return cols
	}

	n, err := strconv.Atoi(strings.TrimSpace(c.Num))
	if err != nil || n < 2 {
		return full
	}
	n = min(n, maxColumns)
	space := dxaToPtOr(c.Space, defaultColumnSpacePt)
	width := (contentWidth - space*float64(n-1)) / float64(n)
	if width <= 0 {
		return full
	}
	cols := make([]Column, n)
	for i := range cols {
		cols[i] = Column{WidthPt: width, SpaceAfterPt: space}
	}
	cols[n-1].SpaceAfterPt = 0
	return cols
}

// headerFooter loads the header/footer part referenced for the given page class
// (default/first/even) and builds its body elements. A reference to a missing or
// malformed part, or no reference at all, yields nil - the page then simply has
// no header/footer.
func headerFooter(refs []xmlHdrFtrRef, class string, p *pkg, sc styleContext) []BodyElement {
	rels := sc.images.rels // the main part's: header references live in it
	for _, ref := range refs {
		if !strings.EqualFold(strings.TrimSpace(ref.Type), class) {
			continue
		}
		name := rels[ref.ID]
		if name == "" {
			continue
		}
		data, err := p.part(name)
		if err != nil {
			continue
		}
		// The root element (w:hdr/w:ftr) holds p/tbl children directly, which is
		// exactly what xmlBody's decoder consumes.
		var body xmlBody
		if xml.Unmarshal(data, &body) != nil {
			continue
		}
		// A header/footer part carries its own relationships (its images) and its
		// own numbering counters, so a list in a header cannot advance - or be
		// advanced by - the body's list counters.
		hsc := sc
		hsc.images = p.resolverFor(name)
		hsc.numbering = sc.numbering.fork()
		elems, _ := buildBody(body, hsc)
		return elems
	}
	return nil
}
