package document

import (
	"cmp"
	"encoding/xml"
	"errors"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// wordNS is the WordprocessingML namespace URI. Element struct tags match on
// this URI plus local name, so any prefix bound to it in the source decodes
// correctly and elements in other namespaces are ignored by encoding/xml —
// which is exactly the "skip unsupported content" behaviour we want.
const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

type xmlDocument struct {
	Body xmlBody `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main body"`
}

// xmlBody captures its p/tbl children in document order via a custom
// UnmarshalXML (encoding/xml has no ordered-union tag); other body children
// (e.g. sectPr) are skipped without error.
type xmlBody struct {
	Children []xmlBodyChild
	// SectPr is the body-level section geometry (w:sectPr), or nil when the
	// document declares none.
	SectPr *xmlSectPr
}

// xmlBodyChild is one body-level child: exactly one of Paragraph or Table is
// non-nil.
type xmlBodyChild struct {
	Paragraph *xmlParagraph
	Table     *xmlTable
}

func (b *xmlBody) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	return decodeBlocks(d, &b.Children, func(start xml.StartElement) (bool, error) {
		if start.Name.Local != "sectPr" {
			return false, nil
		}
		var s xmlSectPr
		if err := d.DecodeElement(&s, &start); err != nil {
			return true, err
		}
		b.SectPr = &s
		return true, nil
	})
}

// decodeBlocks reads the block-level children (w:p, w:tbl) of the element d is
// positioned in, in document order, up to its end tag. Content controls and
// custom XML (w:sdt, w:customXml) are transparent: the blocks inside them are
// read as if they were direct children, since that is where Word puts a table of
// contents or a cover page. other is offered every remaining WordprocessingML
// element and reports whether it consumed it; what it declines is skipped.
func decodeBlocks(d *xml.Decoder, out *[]xmlBodyChild, other func(xml.StartElement) (bool, error)) error {
	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return nil // children were consumed by DecodeElement/Skip
			}
			continue
		}
		if start.Name.Space != wordNS {
			if err := d.Skip(); err != nil {
				return err
			}
			continue
		}
		switch start.Name.Local {
		case "p":
			var p xmlParagraph
			if err := d.DecodeElement(&p, &start); err != nil {
				return err
			}
			*out = append(*out, xmlBodyChild{Paragraph: &p})
		case "tbl":
			var t xmlTable
			if err := d.DecodeElement(&t, &start); err != nil {
				return err
			}
			*out = append(*out, xmlBodyChild{Table: &t})
		case "sdt", "sdtContent", "customXml":
			if err := decodeBlocks(d, out, skipAll); err != nil {
				return err
			}
		default:
			handled, err := other(start)
			if err != nil {
				return err
			}
			if !handled {
				if err := d.Skip(); err != nil {
					return err
				}
			}
		}
	}
}

// skipAll declines every element, for decodeBlocks inside a wrapper whose own
// properties (w:sdtPr, w:customXmlPr) are not modeled.
func skipAll(xml.StartElement) (bool, error) { return false, nil }

// xmlSectPr is a <w:sectPr>: page size (w:pgSz), margins (w:pgMar), column
// layout (w:cols), break type (w:type), and the header/footer parts the section
// references. It appears body-level (the last section's properties) or inside a
// paragraph's w:pPr (ending that section there). w:gutter and even/odd
// header-footer classes are out of scope - see design.md.
type xmlSectPr struct {
	Type       *xmlVal        `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main type"`
	PgSz       *xmlPgSz       `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pgSz"`
	PgMar      *xmlPgMar      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pgMar"`
	Cols       *xmlCols       `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main cols"`
	TitlePg    *xmlOnOff      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main titlePg"`
	HeaderRefs []xmlHdrFtrRef `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main headerReference"`
	FooterRefs []xmlHdrFtrRef `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main footerReference"`
}

// xmlPgSz is <w:pgSz>'s w:w/w:h page dimensions, in dxa (twentieths of a point).
type xmlPgSz struct {
	W string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main w,attr"`
	H string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main h,attr"`
}

// xmlPgMar is <w:pgMar>'s four page margins plus the header's distance from the
// top edge and the footer's from the bottom, in dxa. w:gutter is out of scope.
type xmlPgMar struct {
	Top    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main top,attr"`
	Right  string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main right,attr"`
	Bottom string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main bottom,attr"`
	Left   string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main left,attr"`
	Header string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main header,attr"`
	Footer string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main footer,attr"`
}

// xmlParagraph decodes <w:p> via a custom UnmarshalXML so that runs (<w:r>),
// hyperlinks (<w:hyperlink>) and simple fields (<w:fldSimple>) keep their
// document order in Items: a hyperlink or field sits mid-paragraph, so ordering
// its wrapped runs among the ordinary ones matters for the text to read right.
type xmlParagraph struct {
	Props     *xmlPPr
	Items     []xmlParaChild
	Bookmarks []string
}

// xmlParaChild is one ordered child of a paragraph: exactly one field is non-nil.
type xmlParaChild struct {
	Run       *xmlRun
	Hyperlink *xmlHyperlink
	FldSimple *xmlFldSimple
}

func (xp *xmlParagraph) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return nil
			}
			continue
		}
		if start.Name.Space != wordNS {
			if err := d.Skip(); err != nil {
				return err
			}
			continue
		}
		switch start.Name.Local {
		case "pPr":
			var pr xmlPPr
			if err := d.DecodeElement(&pr, &start); err != nil {
				return err
			}
			xp.Props = &pr
		case "r":
			var r xmlRun
			if err := d.DecodeElement(&r, &start); err != nil {
				return err
			}
			xp.Items = append(xp.Items, xmlParaChild{Run: &r})
		case "hyperlink":
			var h xmlHyperlink
			if err := d.DecodeElement(&h, &start); err != nil {
				return err
			}
			xp.Items = append(xp.Items, xmlParaChild{Hyperlink: &h})
		case "bookmarkStart":
			var b struct {
				Name string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main name,attr"`
			}
			if err := d.DecodeElement(&b, &start); err != nil {
				return err
			}
			if b.Name != "" {
				xp.Bookmarks = append(xp.Bookmarks, b.Name)
			}
		case "fldSimple":
			var fs xmlFldSimple
			if err := d.DecodeElement(&fs, &start); err != nil {
				return err
			}
			xp.Items = append(xp.Items, xmlParaChild{FldSimple: &fs})
		case "ins", "moveTo", "smartTag", "customXml", "sdt", "sdtContent":
			// Wrappers around ordinary runs - a tracked insertion (shown as
			// accepted), a smart tag, a content control: their runs belong to
			// the paragraph in place. w:del and w:moveFrom (removed text) are
			// skipped by the default case.
			if err := xp.UnmarshalXML(d, start); err != nil {
				return err
			}
		default:
			if err := d.Skip(); err != nil {
				return err
			}
		}
	}
}

type xmlPPr struct {
	PStyle          *xmlVal      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pStyle"`
	Jc              *xmlVal      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main jc"`
	PageBreakBefore *xmlOnOff    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pageBreakBefore"`
	Ind             *xmlInd      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main ind"`
	Spacing         *xmlPSpacing `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main spacing"`
	NumPr           *xmlNumPr    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main numPr"`
	Shd             *xmlShd      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main shd"`
	Tabs            *xmlTabs     `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tabs"`
	// SectPr is only meaningful on a body paragraph: it ends a section there.
	SectPr *xmlSectPr `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main sectPr"`
}

// xmlTabs is <w:tabs>: the paragraph's custom tab stops. A w:tab of
// w:val="clear" removes a stop the paragraph would inherit from its style.
type xmlTabs struct {
	Tabs []xmlTab `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tab"`
}

// xmlTab is one <w:tab> stop: its alignment, leader and position (dxa, from
// the text column's left edge).
type xmlTab struct {
	Val    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
	Leader string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main leader,attr"`
	Pos    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pos,attr"`
}

func (t *xmlTabs) list() []xmlTab {
	if t == nil {
		return nil
	}
	return t.Tabs
}

// mergeTabs applies own's stops over inherited ones: a stop at a position
// replaces the inherited stop there, and a "clear" stop removes it. A nil
// result means neither declared any.
func mergeTabs(own, inherited *xmlTabs) *xmlTabs {
	if own == nil {
		return inherited
	}
	var out []xmlTab
	for _, t := range inherited.list() {
		if !slices.ContainsFunc(own.Tabs, func(o xmlTab) bool { return dxaToPt(o.Pos) == dxaToPt(t.Pos) }) {
			out = append(out, t)
		}
	}
	for _, t := range own.Tabs {
		if !strings.EqualFold(strings.TrimSpace(t.Val), "clear") {
			out = append(out, t)
		}
	}
	return &xmlTabs{Tabs: out}
}

// tabStopsFrom resolves merged w:tabs into the paragraph's stops, sorted by
// position. A bar tab is kept as TabBar: the layout never stops at it, only
// draws its rule.
func tabStopsFrom(t *xmlTabs) []TabStop {
	var out []TabStop
	for _, x := range t.list() {
		stop := TabStop{PosPt: dxaToPt(x.Pos), Leader: tabLeaders[strings.ToLower(strings.TrimSpace(x.Leader))]}
		switch strings.ToLower(strings.TrimSpace(x.Val)) {
		case "clear":
			continue
		case "bar":
			stop.Align, stop.Leader = TabBar, ""
		case "center":
			stop.Align = TabCenter
		case "right", "end":
			stop.Align = TabRight
		case "decimal":
			stop.Align = TabDecimal
		default: // "left", "start", "num"
			stop.Align = TabLeft
		}
		out = append(out, stop)
	}
	slices.SortStableFunc(out, func(a, b TabStop) int { return cmp.Compare(a.PosPt, b.PosPt) })
	return out
}

// tabLeaders maps w:tab/@w:leader onto the character the leader repeats.
var tabLeaders = map[string]string{
	"dot":        ".",
	"hyphen":     "-",
	"underscore": "_",
	"heavy":      "_",
	"middledot":  "·",
}

// xmlNumPr is <w:numPr>: a reference into word/numbering.xml by list id
// (w:numId) and list level (w:ilvl, default 0 when absent).
type xmlNumPr struct {
	Ilvl  *xmlVal `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main ilvl"`
	NumID *xmlVal `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main numId"`
}

// xmlInd is <w:ind>: w:left/w:right/w:firstLine/w:hanging, all in dxa
// (twentieths of a point). w:start/w:end (the RTL-aware aliases) are out of
// scope - see design.md.
type xmlInd struct {
	Left      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main left,attr"`
	Right     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main right,attr"`
	FirstLine string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main firstLine,attr"`
	Hanging   string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main hanging,attr"`
}

// xmlPSpacing is <w:spacing>'s w:before/w:after (space around the paragraph)
// and w:line/w:lineRule (spacing between lines within it), all in dxa. The
// AutoSpacing flags are out of scope - see design.md.
type xmlPSpacing struct {
	Before   string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main before,attr"`
	After    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main after,attr"`
	Line     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main line,attr"`
	LineRule string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lineRule,attr"`
}

// xmlRun captures its rPr plus an ordered sequence of text, break and picture
// children via a custom UnmarshalXML (encoding/xml's separate slices would lose
// the document order of <w:t>, <w:br> and <w:drawing>, which matters for
// in-paragraph line breaks and inline images). Unsupported run children (e.g.
// <w:sym>) are skipped without error.
type xmlRun struct {
	Props   *xmlRPr
	Content []xmlRunContent
}

// xmlRunContent is one ordered item of a run: exactly one field is non-nil.
type xmlRunContent struct {
	Text    *string
	Break   *xmlBr
	Drawing *xmlDrawing
	Pict    *xmlPict
	// FldChar is a complex-field boundary ("begin"/"separate"/"end"); InstrText
	// is a fragment of the field code between begin and separate. Together they
	// carry PAGE/NUMPAGES fields, whose displayed value is computed at render.
	FldChar   *string
	InstrText *string
	// Tab is a <w:tab/>: an advance to the paragraph's next tab stop.
	Tab bool
	// NoteRef is a w:footnoteReference/w:endnoteReference; NoteMark is the
	// w:footnoteRef/w:endnoteRef that shows a note's own number inside it.
	NoteRef  *xmlNoteRef
	NoteMark bool
}

func (r *xmlRun) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return nil // </w:r>: children were consumed by DecodeElement/Skip
			}
			continue
		}
		if start.Name.Space != wordNS {
			if err := d.Skip(); err != nil {
				return err
			}
			continue
		}
		switch start.Name.Local {
		case "rPr":
			var p xmlRPr
			if err := d.DecodeElement(&p, &start); err != nil {
				return err
			}
			r.Props = &p
		case "t":
			var txt xmlText
			if err := d.DecodeElement(&txt, &start); err != nil {
				return err
			}
			s := txt.Value
			r.Content = append(r.Content, xmlRunContent{Text: &s})
		case "br":
			var b xmlBr
			if err := d.DecodeElement(&b, &start); err != nil {
				return err
			}
			r.Content = append(r.Content, xmlRunContent{Break: &b})
		case "cr":
			if err := d.Skip(); err != nil {
				return err
			}
			r.Content = append(r.Content, xmlRunContent{Break: &xmlBr{}})
		case "tab":
			if err := d.Skip(); err != nil {
				return err
			}
			r.Content = append(r.Content, xmlRunContent{Tab: true})
		case "footnoteReference", "endnoteReference":
			var ref xmlNoteRef
			if err := d.DecodeElement(&ref, &start); err != nil {
				return err
			}
			ref.endnote = start.Name.Local == "endnoteReference"
			r.Content = append(r.Content, xmlRunContent{NoteRef: &ref})
		case "footnoteRef", "endnoteRef":
			if err := d.Skip(); err != nil {
				return err
			}
			r.Content = append(r.Content, xmlRunContent{NoteMark: true})
		case "noBreakHyphen":
			if err := d.Skip(); err != nil {
				return err
			}
			s := "-"
			r.Content = append(r.Content, xmlRunContent{Text: &s})
		case "drawing":
			var dr xmlDrawing
			if err := d.DecodeElement(&dr, &start); err != nil {
				return err
			}
			r.Content = append(r.Content, xmlRunContent{Drawing: &dr})
		case "pict":
			var p xmlPict
			if err := d.DecodeElement(&p, &start); err != nil {
				return err
			}
			r.Content = append(r.Content, xmlRunContent{Pict: &p})
		case "fldChar":
			var fc xmlFldChar
			if err := d.DecodeElement(&fc, &start); err != nil {
				return err
			}
			t := fc.Type
			r.Content = append(r.Content, xmlRunContent{FldChar: &t})
		case "instrText":
			var txt xmlText
			if err := d.DecodeElement(&txt, &start); err != nil {
				return err
			}
			s := txt.Value
			r.Content = append(r.Content, xmlRunContent{InstrText: &s})
		default:
			if err := d.Skip(); err != nil {
				return err
			}
		}
	}
}

type xmlRPr struct {
	RStyle    *xmlVal    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rStyle"`
	Bold      *xmlOnOff  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main b"`
	Italic    *xmlOnOff  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main i"`
	Underline *xmlVal    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main u"`
	Fonts     *xmlRFonts `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rFonts"`
	Size      *xmlVal    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main sz"`
	Color     *xmlColor  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main color"`
	Shd       *xmlShd    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main shd"`
	Highlight *xmlVal    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main highlight"`
	VertAlign *xmlVal    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main vertAlign"`
	Position  *xmlVal    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main position"`
}

// xmlColor is <w:color>: an explicit sRGB hex value (w:val, or "auto") and/or a
// theme-scheme reference (w:themeColor) resolved through word/theme/theme1.xml,
// optionally lightened (w:themeTint) or darkened (w:themeShade).
type xmlColor struct {
	Val        string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
	ThemeColor string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeColor,attr"`
	ThemeTint  string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeTint,attr"`
	ThemeShade string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeShade,attr"`
}

// xmlRFonts is <w:rFonts>: an explicit typeface (w:ascii/w:hAnsi) and/or a
// reference into the theme's font scheme (w:asciiTheme/w:hAnsiTheme, e.g.
// "minorHAnsi"), resolved through word/theme/theme1.xml.
type xmlRFonts struct {
	ASCII      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main ascii,attr"`
	HAnsi      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main hAnsi,attr"`
	ASCIITheme string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main asciiTheme,attr"`
	HAnsiTheme string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main hAnsiTheme,attr"`
}

type xmlText struct {
	Value string `xml:",chardata"`
}

type xmlBr struct {
	Type string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main type,attr"`
}

// xmlFldChar is a complex-field boundary: <w:fldChar w:fldCharType="begin"/>,
// "separate" or "end".
type xmlFldChar struct {
	Type string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main fldCharType,attr"`
}

// xmlHyperlink is <w:hyperlink>: its wrapped runs (whose text would otherwise be
// dropped, since they are not direct <w:r> children of the paragraph) and its
// target - an external relationship (r:id), a bookmark (w:anchor), or both.
type xmlHyperlink struct {
	ID     string   `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	Anchor string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main anchor,attr"`
	Runs   []xmlRun `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main r"`
}

// xmlFldSimple is <w:fldSimple w:instr="…">: a self-contained field carrying its
// code in an attribute and its cached result as wrapped runs.
type xmlFldSimple struct {
	Instr string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main instr,attr"`
	Runs  []xmlRun `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main r"`
}

// xmlVal holds a w:val attribute (jc, sz, u, …).
type xmlVal struct {
	Val string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
}

// xmlOnOff is a WordprocessingML toggle property (w:b, w:i, w:pageBreakBefore).
// Presence means "on" unless w:val explicitly negates it.
type xmlOnOff struct {
	Val string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
}

func (o *xmlOnOff) on() bool {
	if o == nil {
		return false
	}
	switch strings.ToLower(o.Val) {
	case "false", "0", "off":
		return false
	}
	return true
}

// --- Tables (w:tbl) ---

type xmlTable struct {
	Grid  *xmlTblGrid `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblGrid"`
	Props *xmlTblPr   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblPr"`
	Rows  []xmlRow    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tr"`
}

type xmlTblPr struct {
	StyleID *xmlVal       `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblStyle"`
	Borders *xmlBorders   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblBorders"`
	Ind     *xmlTblInd    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblInd"`
	Look    *xmlTblLook   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblLook"`
	CellMar *xmlTcMar     `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblCellMar"`
	Layout  *xmlTblLayout `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblLayout"`
	TblW    *xmlTblWidth  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblW"`
	Float   *xmlTblPPr    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblpPr"`
}

// xmlTblPPr is <w:tblpPr>: a floating table's offset from - or alignment
// against - its horizontal and vertical anchors, and the distances the text
// wrapping around it keeps.
type xmlTblPPr struct {
	X          string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblpX,attr"`
	Y          string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblpY,attr"`
	XSpec      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblpXSpec,attr"`
	YSpec      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblpYSpec,attr"`
	HorzAnchor string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main horzAnchor,attr"`
	VertAnchor string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main vertAnchor,attr"`
	// The *FromText attributes are the clearance the text keeps, in dxa.
	LeftFromText   string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main leftFromText,attr"`
	RightFromText  string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rightFromText,attr"`
	TopFromText    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main topFromText,attr"`
	BottomFromText string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main bottomFromText,attr"`
}

// xmlTblLayout is <w:tblLayout>: "fixed" (the declared widths are final) or
// "autofit" (the default: undeclared widths come from the content).
type xmlTblLayout struct {
	Type string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main type,attr"`
}

// xmlTcMar is a cell's inner padding: <w:tcMar> on a cell, or <w:tblCellMar> on
// a table (or its style), whose four sides carry a width and a unit.
type xmlTcMar struct {
	Top    *xmlTblWidth `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main top"`
	Bottom *xmlTblWidth `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main bottom"`
	Left   *xmlTblWidth `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main left"`
	Right  *xmlTblWidth `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main right"`
}

// xmlTblWidth is the w:w/w:type pair shared by w:tcW and the w:tcMar sides.
type xmlTblWidth struct {
	W    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main w,attr"`
	Type string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main type,attr"`
}

// xmlTblLook is <w:tblLook>: which conditional table-style regions are active.
// Attributes are the modern form; legacy w:val bitmasks are also accepted.
type xmlTblLook struct {
	Val         string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
	FirstRow    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main firstRow,attr"`
	LastRow     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lastRow,attr"`
	FirstColumn string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main firstColumn,attr"`
	LastColumn  string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lastColumn,attr"`
	NoHBand     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main noHBand,attr"`
	NoVBand     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main noVBand,attr"`
}

// xmlTblInd is <w:tblInd>: the table's own left indent from its containing
// frame, in dxa. w:type is not distinguished (the schema only really uses
// "dxa" in practice) - see design.md.
type xmlTblInd struct {
	W string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main w,attr"`
}

type xmlTblGrid struct {
	Cols []xmlGridCol `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main gridCol"`
}

type xmlGridCol struct {
	W string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main w,attr"`
}

type xmlRow struct {
	Props *xmlTrPr  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main trPr"`
	Cells []xmlCell `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tc"`
}

// xmlTrPr is <w:trPr>: whether the row repeats on every page the table
// continues onto (w:tblHeader) and its declared height (w:trHeight).
type xmlTrPr struct {
	TblHeader *xmlOnOff    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblHeader"`
	Height    *xmlTrHeight `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main trHeight"`
}

// xmlTrHeight is <w:trHeight>: a height in dxa (w:val) and how it binds
// (w:hRule = auto/atLeast/exact).
type xmlTrHeight struct {
	Val   string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
	HRule string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main hRule,attr"`
}

// xmlCell is a <w:tc>: its properties plus its paragraphs and nested tables in
// document order.
type xmlCell struct {
	Props    *xmlTcPr
	Children []xmlBodyChild
}

func (c *xmlCell) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	return decodeBlocks(d, &c.Children, func(start xml.StartElement) (bool, error) {
		if start.Name.Local != "tcPr" {
			return false, nil
		}
		var pr xmlTcPr
		if err := d.DecodeElement(&pr, &start); err != nil {
			return true, err
		}
		c.Props = &pr
		return true, nil
	})
}

type xmlTcPr struct {
	GridSpan *xmlVal      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main gridSpan"`
	VMerge   *xmlVMerge   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main vMerge"`
	Borders  *xmlBorders  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tcBorders"`
	Shd      *xmlShd      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main shd"`
	TcW      *xmlTblWidth `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tcW"`
	Mar      *xmlTcMar    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tcMar"`
	TextDir  *xmlVal      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main textDirection"`
	VAlign   *xmlVal      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main vAlign"`
}

// xmlVMerge is <w:vMerge/> (continue - w:val absent or "continue") or
// <w:vMerge w:val="restart"/>.
type xmlVMerge struct {
	Val string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
}

// xmlShd is <w:shd>, shared by runs, paragraphs and table cells: a background
// fill (w:fill), a pattern (w:val) and its foreground color (w:color), each of
// which may instead reference a theme slot with an optional tint/shade.
type xmlShd struct {
	Val            string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
	Color          string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main color,attr"`
	Fill           string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main fill,attr"`
	ThemeColor     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeColor,attr"`
	ThemeTint      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeTint,attr"`
	ThemeShade     string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeShade,attr"`
	ThemeFill      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeFill,attr"`
	ThemeFillTint  string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeFillTint,attr"`
	ThemeFillShade string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main themeFillShade,attr"`
}

// xmlBorders is shared by w:tblBorders and w:tcBorders: the four sides, the two
// inside grid lines (which only a table declares), and the two diagonals (which
// only a cell declares).
type xmlBorders struct {
	Top     *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main top"`
	Bottom  *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main bottom"`
	Left    *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main left"`
	Right   *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main right"`
	InsideH *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main insideH"`
	InsideV *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main insideV"`
	TL2BR   *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tl2br"`
	TR2BL   *xmlBorder `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tr2bl"`
}

type xmlBorder struct {
	Val   string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main val,attr"`
	Sz    string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main sz,attr"`
	Color string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main color,attr"`
}

// --- Table styles (word/styles.xml) ---
//
// A table style contributes base w:tblPr/w:tblBorders and w:tcPr/w:shd plus
// conditional w:tblStylePr regions (banded rows/columns, first/last row or
// column), all merged along w:basedOn. Conditional pPr/rPr inside regions
// and corner-only types remain out of scope - see design.md.
type xmlStyles struct {
	DocDefaults *xmlDocDefaults `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main docDefaults"`
	Styles      []xmlStyle      `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main style"`
}

// xmlDocDefaults is <w:docDefaults>: the document-wide paragraph-property
// (w:pPrDefault/w:pPr) and run-property (w:rPrDefault/w:rPr) fallbacks beneath
// any named style.
type xmlDocDefaults struct {
	PPrDefault *xmlPPrDefault `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pPrDefault"`
	RPrDefault *xmlRPrDefault `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rPrDefault"`
}

type xmlPPrDefault struct {
	PPr *xmlPPr `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pPr"`
}

type xmlRPrDefault struct {
	RPr *xmlRPr `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rPr"`
}

type xmlStyle struct {
	Type        string          `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main type,attr"`
	StyleID     string          `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main styleId,attr"`
	BasedOn     *xmlVal         `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main basedOn"`
	TblPr       *xmlTblPr       `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblPr"`
	TcPr        *xmlTcPr        `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tcPr"`
	PPr         *xmlPPr         `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pPr"`
	RPr         *xmlRPr         `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rPr"`
	TblStylePr  []xmlTblStylePr `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblStylePr"`
	RowBandSize *xmlVal         `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblStyleRowBandSize"`
	ColBandSize *xmlVal         `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblStyleColBandSize"`
}

// xmlTblStylePr is one conditional formatting region of a table style: its
// borders and shading, plus the paragraph and run formatting (w:pPr/w:rPr) the
// region gives the cells it covers - which is how a table style makes its header
// row bold or centered.
type xmlTblStylePr struct {
	Type  string    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main type,attr"`
	TblPr *xmlTblPr `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tblPr"`
	TcPr  *xmlTcPr  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main tcPr"`
	PPr   *xmlPPr   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pPr"`
	RPr   *xmlRPr   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rPr"`
}

// knownTblStylePrTypes are the w:tblStylePr/@w:type values we apply: the whole
// table, the banded rows/columns, the first/last row and column, and the four
// corner cells.
var knownTblStylePrTypes = map[string]bool{
	"wholeTable": true,
	"firstRow":   true,
	"lastRow":    true,
	"firstCol":   true,
	"lastCol":    true,
	"band1Horz":  true,
	"band2Horz":  true,
	"band1Vert":  true,
	"band2Vert":  true,
	"nwCell":     true,
	"neCell":     true,
	"swCell":     true,
	"seCell":     true,
}

// resolvedTableStyleRegion holds one tblStylePr region's contribution: its
// table-level borders (from the region's w:tblPr, so its inside grid lines still
// only apply between cells), its cell-level ones (from the region's w:tcPr,
// which land on every cell it covers), its shading, and the paragraph/run
// formatting of those cells.
type resolvedTableStyleRegion struct {
	TblBorders  *xmlBorders
	CellBorders *xmlBorders
	Shd         *xmlShd
	PPr         *xmlPPr
	RPr         *xmlRPr
}

// resolvedTableStyle is a table style's effective border/shading/indent/cell
// margin base, its whole-table paragraph and run formatting, and its conditional
// regions, after following its w:basedOn chain.
type resolvedTableStyle struct {
	// Borders are the style's table-level ones (from its w:tblPr); CellBorders
	// are the ones it puts on every cell (from its w:tcPr).
	Borders     *xmlBorders
	CellBorders *xmlBorders
	Shd         *xmlShd
	Ind         *xmlTblInd
	CellMar     *xmlTcMar
	PPr         *xmlPPr
	RPr         *xmlRPr
	Regions     map[string]resolvedTableStyleRegion
	RowBandSize int
	ColBandSize int
}

// tblLookFlags are the active conditional-formatting switches for one table.
type tblLookFlags struct {
	FirstRow bool
	LastRow  bool
	FirstCol bool
	LastCol  bool
	NoHBand  bool
	NoVBand  bool
}

// buildTableStyles resolves every table style declared in xs (nil-safe: an
// absent or empty word/styles.xml yields an empty map, so callers fall back
// to inline-only resolution exactly as before this existed).
func buildTableStyles(xs *xmlStyles) map[string]resolvedTableStyle {
	resolved := make(map[string]resolvedTableStyle)
	if xs == nil {
		return resolved
	}
	defs := make(map[string]xmlStyle)
	for _, s := range xs.Styles {
		if strings.EqualFold(s.Type, "table") && s.StyleID != "" {
			defs[s.StyleID] = s
		}
	}
	for id := range defs {
		resolved[id] = resolveTableStyle(id, defs, resolved, make(map[string]bool))
	}
	return resolved
}

// resolveTableStyle resolves id's own declarations merged over its w:basedOn
// ancestor chain (nearest style wins per side/field). visiting guards against
// a cyclic basedOn chain; an unresolvable id (missing from defs) resolves to
// the zero value, i.e. no style-level border/shading contribution.
func resolveTableStyle(id string, defs map[string]xmlStyle, cache map[string]resolvedTableStyle, visiting map[string]bool) resolvedTableStyle {
	if rs, ok := cache[id]; ok {
		return rs
	}
	def, ok := defs[id]
	if !ok || visiting[id] {
		return resolvedTableStyle{}
	}
	visiting[id] = true
	defer delete(visiting, id)

	var own resolvedTableStyle
	if def.TblPr != nil {
		own.Borders = def.TblPr.Borders
		own.Ind = def.TblPr.Ind
		own.CellMar = def.TblPr.CellMar
	}
	if def.TcPr != nil {
		own.Shd = def.TcPr.Shd
		own.CellBorders = def.TcPr.Borders
	}
	own.PPr = def.PPr
	own.RPr = def.RPr
	own.Regions = ownTblStylePrRegions(def.TblStylePr)
	own.RowBandSize = parseBandSize(def.RowBandSize)
	own.ColBandSize = parseBandSize(def.ColBandSize)

	var parent resolvedTableStyle
	if def.BasedOn != nil && def.BasedOn.Val != "" {
		parent = resolveTableStyle(def.BasedOn.Val, defs, cache, visiting)
	}
	merged := resolvedTableStyle{
		Borders:     mergeBorders(own.Borders, parent.Borders),
		CellBorders: mergeBorders(own.CellBorders, parent.CellBorders),
		Shd:         coalesceShd(own.Shd, parent.Shd),
		Ind:         coalesceTblInd(own.Ind, parent.Ind),
		CellMar:     mergeTcMar(own.CellMar, parent.CellMar),
		PPr:         mergePPr(own.PPr, parent.PPr),
		RPr:         mergeRPr(own.RPr, parent.RPr),
		Regions:     mergeTblStylePrRegions(own.Regions, parent.Regions),
		RowBandSize: coalesceBandSize(own.RowBandSize, parent.RowBandSize),
		ColBandSize: coalesceBandSize(own.ColBandSize, parent.ColBandSize),
	}
	cache[id] = merged
	return merged
}

func parseBandSize(v *xmlVal) int {
	if v == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v.Val))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func coalesceBandSize(own, parent int) int {
	if own > 0 {
		return own
	}
	if parent > 0 {
		return parent
	}
	return 1
}

func ownTblStylePrRegions(prs []xmlTblStylePr) map[string]resolvedTableStyleRegion {
	if len(prs) == 0 {
		return nil
	}
	out := make(map[string]resolvedTableStyleRegion)
	for _, pr := range prs {
		typ := strings.TrimSpace(pr.Type)
		if !knownTblStylePrTypes[typ] {
			continue
		}
		r := resolvedTableStyleRegion{PPr: pr.PPr, RPr: pr.RPr}
		if pr.TcPr != nil {
			r.CellBorders = pr.TcPr.Borders
			r.Shd = pr.TcPr.Shd
		}
		if pr.TblPr != nil {
			r.TblBorders = pr.TblPr.Borders
		}
		out[typ] = r
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeTblStylePrRegions(own, parent map[string]resolvedTableStyleRegion) map[string]resolvedTableStyleRegion {
	if len(own) == 0 && len(parent) == 0 {
		return nil
	}
	out := make(map[string]resolvedTableStyleRegion)
	maps.Copy(out, parent)
	for k, o := range own {
		p := out[k]
		out[k] = resolvedTableStyleRegion{
			TblBorders:  mergeBorders(o.TblBorders, p.TblBorders),
			CellBorders: mergeBorders(o.CellBorders, p.CellBorders),
			Shd:         coalesceShd(o.Shd, p.Shd),
			PPr:         mergePPr(o.PPr, p.PPr),
			RPr:         mergeRPr(o.RPr, p.RPr),
		}
	}
	return out
}

// mergeTcMar combines two cell-margin declarations per side, own winning; a nil
// result means neither declared anything (so the defaults apply).
func mergeTcMar(own, fallback *xmlTcMar) *xmlTcMar {
	if own == nil && fallback == nil {
		return nil
	}
	var o, f xmlTcMar
	if own != nil {
		o = *own
	}
	if fallback != nil {
		f = *fallback
	}
	return &xmlTcMar{
		Top:    coalesceTblWidth(o.Top, f.Top),
		Bottom: coalesceTblWidth(o.Bottom, f.Bottom),
		Left:   coalesceTblWidth(o.Left, f.Left),
		Right:  coalesceTblWidth(o.Right, f.Right),
	}
}

func coalesceTblWidth(a, b *xmlTblWidth) *xmlTblWidth {
	if a != nil {
		return a
	}
	return b
}

// mergePPr combines two paragraph-property declarations per field, own winning.
// Only the fields a table style can contribute are merged: w:pStyle,
// w:pageBreakBefore and w:sectPr are meaningful on a paragraph itself, not as an
// inherited tier.
func mergePPr(own, fallback *xmlPPr) *xmlPPr {
	if own == nil && fallback == nil {
		return nil
	}
	var o, f xmlPPr
	if own != nil {
		o = *own
	}
	if fallback != nil {
		f = *fallback
	}
	return &xmlPPr{
		Jc:      coalesceVal(o.Jc, f.Jc),
		Ind:     coalesceInd(o.Ind, f.Ind),
		Spacing: mergeSpacing(o.Spacing, f.Spacing),
		NumPr:   coalesceNumPr(o.NumPr, f.NumPr),
		Shd:     coalesceShd(o.Shd, f.Shd),
		Tabs:    mergeTabs(o.Tabs, f.Tabs),
	}
}

// parseTblLook turns w:tblLook into flags. When look is nil, defaults match
// Word's common styled-table look: first/last row and first column on, last
// column off, horizontal banding on, vertical banding off.
func parseTblLook(look *xmlTblLook) tblLookFlags {
	if look == nil {
		return tblLookFlags{
			FirstRow: true,
			LastRow:  true,
			FirstCol: true,
			LastCol:  false,
			NoHBand:  false,
			NoVBand:  true,
		}
	}
	f := tblLookFlags{
		FirstRow: tblLookAttr(look.FirstRow, look.Val, 0x0020),
		LastRow:  tblLookAttr(look.LastRow, look.Val, 0x0040),
		FirstCol: tblLookAttr(look.FirstColumn, look.Val, 0x0080),
		LastCol:  tblLookAttr(look.LastColumn, look.Val, 0x0100),
		NoHBand:  tblLookAttr(look.NoHBand, look.Val, 0x0200),
		NoVBand:  tblLookAttr(look.NoVBand, look.Val, 0x0400),
	}
	// If no modern attrs and no usable val, fall back to defaults.
	if look.FirstRow == "" && look.LastRow == "" && look.FirstColumn == "" &&
		look.LastColumn == "" && look.NoHBand == "" && look.NoVBand == "" &&
		strings.TrimSpace(look.Val) == "" {
		return parseTblLook(nil)
	}
	return f
}

func tblLookAttr(attr, valHex string, bit uint16) bool {
	if attr != "" {
		switch strings.ToLower(strings.TrimSpace(attr)) {
		case "1", "true", "on":
			return true
		case "0", "false", "off":
			return false
		}
	}
	v := strings.TrimSpace(valHex)
	if v == "" {
		return false
	}
	v = strings.TrimPrefix(strings.ToLower(v), "0x")
	n, err := strconv.ParseUint(v, 16, 16)
	if err != nil {
		return false
	}
	return uint16(n)&bit != 0
}

// mergeBorders combines two xmlBorders per side, own winning; a nil result
// means neither declared anything.
func mergeBorders(own, fallback *xmlBorders) *xmlBorders {
	if own == nil && fallback == nil {
		return nil
	}
	var o, f xmlBorders
	if own != nil {
		o = *own
	}
	if fallback != nil {
		f = *fallback
	}
	return &xmlBorders{
		Top:     coalesceBorder(o.Top, f.Top),
		Bottom:  coalesceBorder(o.Bottom, f.Bottom),
		Left:    coalesceBorder(o.Left, f.Left),
		Right:   coalesceBorder(o.Right, f.Right),
		InsideH: coalesceBorder(o.InsideH, f.InsideH),
		InsideV: coalesceBorder(o.InsideV, f.InsideV),
		TL2BR:   coalesceBorder(o.TL2BR, f.TL2BR),
		TR2BL:   coalesceBorder(o.TR2BL, f.TR2BL),
	}
}

func coalesceBorder(a, b *xmlBorder) *xmlBorder {
	if a != nil {
		return a
	}
	return b
}

func coalesceShd(a, b *xmlShd) *xmlShd {
	if a != nil {
		return a
	}
	return b
}

func coalesceTblInd(a, b *xmlTblInd) *xmlTblInd {
	if a != nil {
		return a
	}
	return b
}

// --- Paragraph & character styles (word/styles.xml) ---
//
// Mirrors the table-style machinery above: a paragraph style's own w:pPr
// (spacing, alignment, indent, numbering reference) and w:rPr are resolved
// along its w:basedOn chain, and character styles' w:rPr likewise.

// resolvedParaStyle is a paragraph style's effective paragraph and run
// property base, after following its w:basedOn chain (nearest style wins per
// field).
type resolvedParaStyle struct {
	Spacing *xmlPSpacing
	Jc      *xmlVal
	Ind     *xmlInd
	NumPr   *xmlNumPr
	Shd     *xmlShd
	Tabs    *xmlTabs
	RPr     *xmlRPr
}

// buildParaStyles resolves every paragraph style declared in xs (nil-safe:
// an absent styles.xml yields an empty map, so callers fall back to inline/
// docDefaults resolution).
func buildParaStyles(xs *xmlStyles) map[string]resolvedParaStyle {
	resolved := make(map[string]resolvedParaStyle)
	if xs == nil {
		return resolved
	}
	defs := make(map[string]xmlStyle)
	for _, s := range xs.Styles {
		if strings.EqualFold(s.Type, "paragraph") && s.StyleID != "" {
			defs[s.StyleID] = s
		}
	}
	for id := range defs {
		resolved[id] = resolveParaStyle(id, defs, resolved, make(map[string]bool))
	}
	return resolved
}

// resolveParaStyle resolves id's own spacing merged over its w:basedOn ancestor
// chain (nearest style wins per field). visiting guards against a cyclic
// basedOn chain; an unresolvable id resolves to the zero value.
func resolveParaStyle(id string, defs map[string]xmlStyle, cache map[string]resolvedParaStyle, visiting map[string]bool) resolvedParaStyle {
	if rs, ok := cache[id]; ok {
		return rs
	}
	def, ok := defs[id]
	if !ok || visiting[id] {
		return resolvedParaStyle{}
	}
	visiting[id] = true
	defer delete(visiting, id)

	var own resolvedParaStyle
	if def.PPr != nil {
		own.Spacing = def.PPr.Spacing
		own.Jc = def.PPr.Jc
		own.Ind = def.PPr.Ind
		own.NumPr = def.PPr.NumPr
		own.Shd = def.PPr.Shd
		own.Tabs = def.PPr.Tabs
	}
	own.RPr = def.RPr
	var parent resolvedParaStyle
	if def.BasedOn != nil && def.BasedOn.Val != "" {
		parent = resolveParaStyle(def.BasedOn.Val, defs, cache, visiting)
	}
	merged := resolvedParaStyle{
		Spacing: mergeSpacing(own.Spacing, parent.Spacing),
		Jc:      coalesceVal(own.Jc, parent.Jc),
		Ind:     coalesceInd(own.Ind, parent.Ind),
		NumPr:   coalesceNumPr(own.NumPr, parent.NumPr),
		Shd:     coalesceShd(own.Shd, parent.Shd),
		Tabs:    mergeTabs(own.Tabs, parent.Tabs),
		RPr:     mergeRPr(own.RPr, parent.RPr),
	}
	cache[id] = merged
	return merged
}

// buildCharStyles resolves every character style's w:rPr along its w:basedOn
// chain, keyed by w:styleId (nil-safe: an absent styles.xml yields an empty
// map). A run's w:rStyle is looked up here.
func buildCharStyles(xs *xmlStyles) map[string]*xmlRPr {
	resolved := make(map[string]*xmlRPr)
	if xs == nil {
		return resolved
	}
	defs := make(map[string]xmlStyle)
	for _, s := range xs.Styles {
		if strings.EqualFold(s.Type, "character") && s.StyleID != "" {
			defs[s.StyleID] = s
		}
	}
	for id := range defs {
		resolved[id] = resolveCharStyle(id, defs, resolved, make(map[string]bool))
	}
	return resolved
}

// resolveCharStyle merges id's own w:rPr over its w:basedOn ancestor chain
// (nearest wins per field). visiting guards against a cyclic chain; an
// unresolvable id resolves to nil (no character-style contribution).
func resolveCharStyle(id string, defs map[string]xmlStyle, cache map[string]*xmlRPr, visiting map[string]bool) *xmlRPr {
	if rs, ok := cache[id]; ok {
		return rs
	}
	def, ok := defs[id]
	if !ok || visiting[id] {
		return nil
	}
	visiting[id] = true
	defer delete(visiting, id)

	var parent *xmlRPr
	if def.BasedOn != nil && def.BasedOn.Val != "" {
		parent = resolveCharStyle(def.BasedOn.Val, defs, cache, visiting)
	}
	merged := mergeRPr(def.RPr, parent)
	cache[id] = merged
	return merged
}

// mergeRPr combines two xmlRPr per field, own winning where it declares a
// value; a nil result means neither declared anything. Toggle fields (w:b,
// w:i) coalesce by presence, so an explicit off (w:val="false") on own is a
// non-nil pointer that wins over the parent. w:rStyle is not merged - it is
// only meaningful on the inline run that carries the reference.
func mergeRPr(own, fallback *xmlRPr) *xmlRPr {
	if own == nil && fallback == nil {
		return nil
	}
	var o, f xmlRPr
	if own != nil {
		o = *own
	}
	if fallback != nil {
		f = *fallback
	}
	return &xmlRPr{
		Bold:      coalesceOnOff(o.Bold, f.Bold),
		Italic:    coalesceOnOff(o.Italic, f.Italic),
		Underline: coalesceVal(o.Underline, f.Underline),
		Fonts:     coalesceFonts(o.Fonts, f.Fonts),
		Size:      coalesceVal(o.Size, f.Size),
		Color:     coalesceColor(o.Color, f.Color),
		Shd:       coalesceShd(o.Shd, f.Shd),
		Highlight: coalesceVal(o.Highlight, f.Highlight),
		VertAlign: coalesceVal(o.VertAlign, f.VertAlign),
		Position:  coalesceVal(o.Position, f.Position),
	}
}

func coalesceVal(a, b *xmlVal) *xmlVal {
	if a != nil {
		return a
	}
	return b
}

func coalesceOnOff(a, b *xmlOnOff) *xmlOnOff {
	if a != nil {
		return a
	}
	return b
}

func coalesceInd(a, b *xmlInd) *xmlInd {
	if a != nil {
		return a
	}
	return b
}

func coalesceNumPr(a, b *xmlNumPr) *xmlNumPr {
	if a != nil {
		return a
	}
	return b
}

func coalesceFonts(a, b *xmlRFonts) *xmlRFonts {
	if a != nil {
		return a
	}
	return b
}

func coalesceColor(a, b *xmlColor) *xmlColor {
	if a != nil {
		return a
	}
	return b
}

// mergeSpacing combines two xmlPSpacing per field (before/after), own winning
// where it declares a value; a nil result means neither declared anything.
// Structured like mergeBorders.
func mergeSpacing(own, fallback *xmlPSpacing) *xmlPSpacing {
	if own == nil && fallback == nil {
		return nil
	}
	var o, f xmlPSpacing
	if own != nil {
		o = *own
	}
	if fallback != nil {
		f = *fallback
	}
	merged := xmlPSpacing{
		Before: coalesceStr(o.Before, f.Before),
		After:  coalesceStr(o.After, f.After),
	}
	// Line and LineRule travel as a pair: a rule must not be split from its
	// value, so whichever source declares w:line supplies both.
	if o.Line != "" {
		merged.Line, merged.LineRule = o.Line, o.LineRule
	} else {
		merged.Line, merged.LineRule = f.Line, f.LineRule
	}
	return &merged
}

func coalesceStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- Builders: xml* -> the public document model ---

// styleContext bundles the resolved style data threaded through the builders:
// table styles (borders/shading/indent), paragraph styles, character styles,
// the document-wide w:docDefaults paragraph/run properties, the theme color and
// font maps, the image resolver for the part being built, and the mutable
// list-numbering state (nil when the document has no lists).
// tablePPr/tableRPr are set only while a table cell's paragraphs are built: the
// formatting its table style (and the conditional regions covering it) give the
// cell, a tier below the paragraph/character styles and above w:docDefaults.
type styleContext struct {
	tables     map[string]resolvedTableStyle
	paragraphs map[string]resolvedParaStyle
	chars      map[string]*xmlRPr
	defaultPPr *xmlPPr
	defaultRPr *xmlRPr
	tablePPr   *xmlPPr
	tableRPr   *xmlRPr
	theme      map[string]string
	themeFonts map[string]string
	images     *imageResolver
	numbering  *numberingState
	// defaultTabPt is word/settings.xml's w:defaultTabStop, in points (0 when
	// the document declares none).
	defaultTabPt float64
	// footnotes/endnotes number and build the notes the body references (nil
	// for a document without them, and inside a note); noteMark is the number
	// of the note being built, which its w:footnoteRef/w:endnoteRef shows.
	footnotes, endnotes *noteSource
	noteMark            string
}

// underPPr is the paragraph-property tier beneath the named styles: the table
// style's contribution (when building a cell) over w:docDefaults.
func (sc styleContext) underPPr() *xmlPPr {
	if sc.defaultPPr == nil {
		return sc.tablePPr
	}
	return mergePPr(sc.tablePPr, sc.defaultPPr)
}

// underRPr is the run-property tier beneath the named styles: the table style's
// contribution over w:docDefaults.
func (sc styleContext) underRPr() *xmlRPr {
	return mergeRPr(sc.tableRPr, sc.defaultRPr)
}

// buildBody converts a body (or a header/footer part, which holds the same
// children) into body elements, and reports every mid-body section break: a
// paragraph whose w:pPr carries a w:sectPr ends a section right after it.
func buildBody(body xmlBody, sc styleContext) ([]BodyElement, []sectionBreak) {
	elems := make([]BodyElement, 0, len(body.Children))
	var breaks []sectionBreak
	for _, c := range body.Children {
		switch {
		case c.Paragraph != nil:
			p := buildParagraph(*c.Paragraph, sc)
			elems = append(elems, BodyElement{Paragraph: &p})
			if c.Paragraph.Props != nil && c.Paragraph.Props.SectPr != nil {
				breaks = append(breaks, sectionBreak{props: c.Paragraph.Props.SectPr, end: len(elems)})
			}
		case c.Table != nil:
			t := buildTable(*c.Table, sc)
			elems = append(elems, BodyElement{Table: &t})
		}
	}
	return elems, breaks
}

func buildParagraph(xp xmlParagraph, sc styleContext) Paragraph {
	var p Paragraph
	var inlinePPr *xmlPPr
	var pStyleID string
	if xp.Props != nil {
		inlinePPr = xp.Props
		p.Props.PageBreak = xp.Props.PageBreakBefore.on()
		if xp.Props.PStyle != nil {
			pStyleID = xp.Props.PStyle.Val
		}
	}
	ps, hasStyle := sc.paragraphs[pStyleID]
	// under is the tier beneath the named styles: the table style's cell
	// formatting (inside a table) over w:docDefaults.
	under := sc.underPPr()

	// Alignment: inline w:jc → paragraph style → table style → docDefaults → left.
	jc := inlinePPr.jc()
	if jc == nil && hasStyle {
		jc = ps.Jc
	}
	if jc == nil && under != nil {
		jc = under.Jc
	}
	p.Props.Alignment = alignmentFrom(jc)

	// Indent: inline w:ind → paragraph style → table style → docDefaults. A nil
	// result means the paragraph declared no indent, in which case a list level
	// may supply its own (typically hanging) indent below.
	ind := inlinePPr.ind()
	if ind == nil && hasStyle {
		ind = ps.Ind
	}
	if ind == nil && under != nil {
		ind = under.Ind
	}

	p.Props.Spacing = resolveSpacing(inlinePPr.spacing(), pStyleID, sc)

	// Shading: inline w:shd → paragraph style → table style → docDefaults.
	shd := inlinePPr.shd()
	if shd == nil && hasStyle {
		shd = ps.Shd
	}
	if shd == nil && under != nil {
		shd = under.Shd
	}
	p.Props.Shading = resolveShading(shd, sc.theme)

	// Tab stops accumulate down the tiers rather than one tier winning:
	// docDefaults/table style, then the paragraph style, then the paragraph.
	var tabs *xmlTabs
	if under != nil {
		tabs = under.Tabs
	}
	if hasStyle {
		tabs = mergeTabs(ps.Tabs, tabs)
	}
	if inlinePPr != nil {
		tabs = mergeTabs(inlinePPr.Tabs, tabs)
	}
	p.Props.Tabs = tabStopsFrom(tabs)
	p.Props.DefaultTabStopPt = sc.defaultTabPt

	// Numbering: prepend a marker run and (when the paragraph declared no
	// indent) borrow the level's hanging indent.
	if m, ok := resolveMarker(inlinePPr, ps, hasStyle, sc); ok {
		if ind == nil {
			ind = m.ind
		}
		p.Runs = append(p.Runs, markerRun(m, indentFrom(ind), sc))
	}
	p.Props.Indent = indentFrom(ind)

	var pStyleRPr *xmlRPr
	if hasStyle {
		pStyleRPr = ps.RPr
	}
	// A page/column break before any content (the list marker aside) belongs to
	// the paragraph; one after content is a run, so the content before it stays
	// on the current page.
	fld := fieldState{lead: len(p.Runs)}
	for _, item := range xp.Items {
		switch {
		case item.Run != nil:
			appendRun(&p, *item.Run, pStyleRPr, sc, &fld)
		case item.Hyperlink != nil:
			// A hyperlink's runs carry the "Hyperlink" character style, so they
			// pick up its blue/underline through the ordinary rStyle resolution;
			// surfacing them here is what stops the link text from vanishing.
			from := len(p.Runs)
			for _, xr := range item.Hyperlink.Runs {
				appendRun(&p, xr, pStyleRPr, sc, &fld)
			}
			if target := sc.images.linkTarget(item.Hyperlink.ID, item.Hyperlink.Anchor); target != "" {
				for i := from; i < len(p.Runs); i++ {
					p.Runs[i].Link = target
				}
			}
		case item.FldSimple != nil:
			appendSimpleField(&p, *item.FldSimple, pStyleRPr, sc, fld.lead)
		}
	}
	p.Bookmarks = xp.Bookmarks
	return p
}

// fieldState tracks a complex field (fldChar begin…separate…end) as buildParagraph
// walks a paragraph's runs. Between begin and separate the field code is
// collected; once it is known to be PAGE/NUMPAGES a single computed run is
// emitted and the cached result runs (up to end) are suppressed. Any other field
// leaves suppress false, so its cached result flows as ordinary text. lead is
// how many runs (a list marker) precede the paragraph's own content; it is
// paragraph-wide and survives the reset at each field's begin/end.
type fieldState struct {
	active   bool
	inInstr  bool
	suppress bool
	emitted  bool
	instr    strings.Builder
	lead     int
}

// appendRun turns one <w:r> into paragraph runs, feeding its content through the
// field state machine so PAGE/NUMPAGES fields become computed runs.
func appendRun(p *Paragraph, xr xmlRun, pStyleRPr *xmlRPr, sc styleContext, fld *fieldState) {
	props := resolveRunProps(xr.Props, pStyleRPr, sc)
	for _, c := range xr.Content {
		switch {
		case c.FldChar != nil:
			switch strings.ToLower(strings.TrimSpace(*c.FldChar)) {
			case "begin":
				*fld = fieldState{active: true, inInstr: true, lead: fld.lead}
			case "separate":
				fld.inInstr = false
				if fld.active && !fld.emitted {
					if k := classifyField(fld.instr.String()); k != "" {
						p.Runs = append(p.Runs, Run{Field: k, Props: props})
						fld.emitted, fld.suppress = true, true
					}
				}
			case "end":
				if fld.active && !fld.emitted {
					if k := classifyField(fld.instr.String()); k != "" {
						p.Runs = append(p.Runs, Run{Field: k, Props: props})
					}
				}
				*fld = fieldState{lead: fld.lead}
			}
		case c.InstrText != nil:
			if fld.active && fld.inInstr {
				fld.instr.WriteString(*c.InstrText)
			}
		case fld.active && (fld.inInstr || fld.suppress):
			// Instruction region, or a known field's cached result: draw nothing.
		case c.Break != nil:
			atStart := len(p.Runs) == fld.lead
			switch strings.ToLower(strings.TrimSpace(c.Break.Type)) {
			case "page":
				if atStart {
					p.Props.PageBreak = true
				} else {
					p.Runs = append(p.Runs, Run{PageBreak: true})
				}
			case "column":
				if atStart {
					p.Props.ColumnBreak = true
				} else {
					p.Runs = append(p.Runs, Run{ColumnBreak: true})
				}
			default:
				p.Runs = append(p.Runs, Run{LineBreak: true})
			}
		case c.Drawing != nil:
			if img := c.Drawing.image(sc.images); img != nil {
				p.Runs = append(p.Runs, Run{Image: img, Props: props})
			}
		case c.Pict != nil:
			if img := c.Pict.image(sc.images); img != nil {
				p.Runs = append(p.Runs, Run{Image: img, Props: props})
			}
		case c.Tab:
			p.Runs = append(p.Runs, Run{Tab: true, Props: props})
		case c.NoteRef != nil:
			ns := sc.footnotes
			if c.NoteRef.endnote {
				ns = sc.endnotes
			}
			if note, mark := ns.reference(c.NoteRef.ID, sc); note != nil {
				p.Runs = append(p.Runs, Run{Text: mark, Props: noteMarkProps(props), Note: note})
			}
		case c.NoteMark && sc.noteMark != "":
			p.Runs = append(p.Runs, Run{Text: sc.noteMark, Props: noteMarkProps(props)})
		case c.Text != nil && *c.Text != "":
			p.Runs = append(p.Runs, Run{Text: *c.Text, Props: props})
		}
	}
}

// appendSimpleField turns a <w:fldSimple> into a computed run for PAGE/NUMPAGES,
// or - for any other field - surfaces its cached result runs as ordinary text.
func appendSimpleField(p *Paragraph, fs xmlFldSimple, pStyleRPr *xmlRPr, sc styleContext, lead int) {
	if k := classifyField(fs.Instr); k != "" {
		props := resolveRunProps(nil, pStyleRPr, sc)
		if len(fs.Runs) > 0 {
			props = resolveRunProps(fs.Runs[0].Props, pStyleRPr, sc)
		}
		p.Runs = append(p.Runs, Run{Field: k, Props: props})
		return
	}
	ignored := fieldState{lead: lead}
	for _, xr := range fs.Runs {
		appendRun(p, xr, pStyleRPr, sc, &ignored)
	}
}

// classifyField returns FieldPage or FieldNumPages for a field code whose first
// keyword is PAGE or NUMPAGES (ignoring switches like \* MERGEFORMAT), else "".
func classifyField(instr string) string {
	f := strings.Fields(instr)
	if len(f) == 0 {
		return ""
	}
	switch strings.ToUpper(f[0]) {
	case FieldPage:
		return FieldPage
	case FieldNumPages:
		return FieldNumPages
	}
	return ""
}

// markerRun turns a resolved list marker into the run that precedes the
// paragraph's own runs: a picture-bullet image, or the marker text followed by
// its w:suff separator. For the default "tab" suffix the marker's advance is
// widened to the level's hanging indent, so the text after it starts exactly at
// the paragraph's left indent - the tab stop Word aligns it to.
func markerRun(m marker, ind Indent, sc styleContext) Run {
	props := runPropsFrom(mergeRPr(m.rpr, sc.underRPr()), sc)
	if m.image != nil {
		r := Run{Image: m.image, Props: props}
		r.MinWidthPt = markerAdvance(m.suff, ind, sc.defaultTabPt)
		return r
	}
	text := m.text
	if strings.EqualFold(m.suff, "space") {
		text += " "
	}
	return Run{Text: text, Props: props, MinWidthPt: markerAdvance(m.suff, ind, sc.defaultTabPt)}
}

// defaultTabStopPt is the tab interval used when a list marker's tab suffix has
// no hanging indent to align to (720 dxa = 0.5", Word's w:defaultTabStop).
const defaultTabStopPt = 36.0

// markerAdvance is the minimum width a marker run occupies. Only the "tab"
// suffix (the default) reserves space: up to the level's hanging indent, or one
// default tab stop (defaultTab, or Word's half inch when that is zero) when the
// level declares none.
//
// ponytail: a marker wider than that advance is simply followed by the text,
// where Word would jump to the next tab stop; add real tab stops if a list ever
// needs them.
func markerAdvance(suff string, ind Indent, defaultTab float64) float64 {
	switch strings.ToLower(strings.TrimSpace(suff)) {
	case "space", "nothing":
		return 0
	}
	if ind.FirstLineOffsetPt < 0 { // a hanging indent: the text resumes at LeftPt
		return -ind.FirstLineOffsetPt
	}
	return cmp.Or(defaultTab, defaultTabStopPt)
}

// jc/ind/spacing are nil-safe accessors for a paragraph's inline w:pPr fields.
func (p *xmlPPr) jc() *xmlVal {
	if p == nil {
		return nil
	}
	return p.Jc
}

func (p *xmlPPr) ind() *xmlInd {
	if p == nil {
		return nil
	}
	return p.Ind
}

func (p *xmlPPr) spacing() *xmlPSpacing {
	if p == nil {
		return nil
	}
	return p.Spacing
}

func (p *xmlPPr) shd() *xmlShd {
	if p == nil {
		return nil
	}
	return p.Shd
}

// resolveMarker resolves a paragraph's list marker: it finds the numbering
// reference (inline w:numPr → paragraph style's numPr) and advances that list's
// document-order counter. ok is false when the paragraph is not a list item or
// the reference resolves to no known level.
func resolveMarker(inlinePPr *xmlPPr, ps resolvedParaStyle, hasStyle bool, sc styleContext) (marker, bool) {
	if sc.numbering == nil {
		return marker{}, false
	}
	var np *xmlNumPr
	if inlinePPr != nil {
		np = inlinePPr.NumPr
	}
	if np == nil && hasStyle {
		np = ps.NumPr
	}
	if np == nil || np.NumID == nil {
		return marker{}, false
	}
	numID := strings.TrimSpace(np.NumID.Val)
	if numID == "" {
		return marker{}, false
	}
	ilvl := 0
	if np.Ilvl != nil {
		if v, err := strconv.Atoi(strings.TrimSpace(np.Ilvl.Val)); err == nil && v >= 0 {
			ilvl = v
		}
	}
	return sc.numbering.advance(numID, ilvl)
}

func alignmentFrom(jc *xmlVal) Alignment {
	if jc == nil {
		return AlignLeft
	}
	switch strings.ToLower(jc.Val) {
	case "center":
		return AlignCenter
	case "right", "end":
		return AlignRight
	case "both", "distribute", "justify":
		return AlignJustify
	default:
		return AlignLeft
	}
}

// indentFrom resolves w:ind into an Indent, folding w:firstLine/w:hanging
// into one signed FirstLineOffsetPt (positive/negative respectively; zero
// when neither is declared, or when ind is nil).
func indentFrom(ind *xmlInd) Indent {
	if ind == nil {
		return Indent{}
	}
	var firstLine float64
	switch {
	case ind.FirstLine != "":
		firstLine = dxaToPt(ind.FirstLine)
	case ind.Hanging != "":
		firstLine = -dxaToPt(ind.Hanging)
	}
	return Indent{
		LeftPt:            dxaToPt(ind.Left),
		RightPt:           dxaToPt(ind.Right),
		FirstLineOffsetPt: firstLine,
	}
}

// resolveSpacing resolves a paragraph's effective spacing per field: inline
// w:spacing wins, else the referenced paragraph style's resolved spacing
// (looked up by w:pStyle), else the table style's (inside a cell), else the
// document defaults, else zero.
func resolveSpacing(inline *xmlPSpacing, pStyleID string, sc styleContext) Spacing {
	var styleThenDefault *xmlPSpacing
	if under := sc.underPPr(); under != nil {
		styleThenDefault = under.Spacing
	}
	if ps, ok := sc.paragraphs[pStyleID]; ok {
		styleThenDefault = mergeSpacing(ps.Spacing, styleThenDefault)
	}
	return spacingFrom(mergeSpacing(inline, styleThenDefault))
}

// spacingFrom resolves w:spacing into a Spacing (zero value when sp is nil).
func spacingFrom(sp *xmlPSpacing) Spacing {
	if sp == nil {
		return Spacing{}
	}
	rule, val := lineSpacingFrom(sp.Line, sp.LineRule)
	return Spacing{
		BeforePt:  dxaToPt(sp.Before),
		AfterPt:   dxaToPt(sp.After),
		LineRule:  rule,
		LineValue: val,
	}
}

// lineSpacingFrom resolves w:line/w:lineRule into a rule and its value: a
// multiple for "auto" (w:line in 240ths of a line, e.g. 276 → 1.15), or a
// point height for "exact"/"atLeast" (w:line in dxa). An absent w:lineRule
// with a w:line defaults to "auto" (per OOXML); an absent, unparseable or
// non-positive w:line yields the single (default) rule, since a zero line height
// would draw every line of the paragraph on top of the first.
func lineSpacingFrom(line, rule string) (LineSpacingRule, float64) {
	v, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
	if err != nil || v <= 0 {
		return LineSpacingSingle, 0
	}
	switch strings.ToLower(strings.TrimSpace(rule)) {
	case "exact":
		return LineSpacingExact, v / 20
	case "atleast":
		return LineSpacingAtLeast, v / 20
	default: // "auto" or absent
		return LineSpacingMultiple, v / 240
	}
}

// resolveRunProps resolves a run's effective character formatting per field:
// inline w:rPr → its w:rStyle character style (basedOn-merged) → the paragraph
// style's w:rPr → w:rPrDefault → hardcoded defaults.
func resolveRunProps(inline *xmlRPr, pStyleRPr *xmlRPr, sc styleContext) RunProperties {
	var charRPr *xmlRPr
	if inline != nil && inline.RStyle != nil {
		if id := strings.TrimSpace(inline.RStyle.Val); id != "" {
			charRPr = sc.chars[id] // nil for an unknown style id (tier skipped)
		}
	}
	merged := mergeRPr(inline, mergeRPr(charRPr, mergeRPr(pStyleRPr, sc.underRPr())))
	return runPropsFrom(merged, sc)
}

// runPropsFrom turns an already-merged w:rPr into public RunProperties,
// filling unset fields with the hardcoded defaults and resolving colors, the
// font (which may be a theme reference) and the run's background through the
// theme maps.
func runPropsFrom(rpr *xmlRPr, sc styleContext) RunProperties {
	props := RunProperties{
		FontFamily: defaultFontFamily,
		SizePt:     defaultFontSizePt,
	}
	if rpr == nil {
		return props
	}
	props.Bold = rpr.Bold.on()
	props.Italic = rpr.Italic.on()
	props.Underline = underlineOn(rpr.Underline)
	if name := resolveFontFamily(rpr.Fonts, sc.themeFonts); name != "" {
		props.FontFamily = name
	}
	if rpr.Size != nil {
		if hp, err := strconv.ParseFloat(strings.TrimSpace(rpr.Size.Val), 64); err == nil && hp > 0 {
			props.SizePt = hp / 2 // half-points → points
		}
	}
	props.Color = resolveColor(rpr.Color, sc.theme)
	// A highlight sits on top of any shading, so it wins when a run carries both.
	props.Shading = firstNonEmpty(highlightHex(rpr.Highlight), resolveShading(rpr.Shd, sc.theme))
	if rpr.Position != nil {
		if hp, err := strconv.ParseFloat(strings.TrimSpace(rpr.Position.Val), 64); err == nil {
			props.BaselineShiftPt = hp / 2 // half-points → points
		}
	}
	if rpr.VertAlign != nil {
		// Word sets superscript and subscript at about two thirds of the size,
		// raised by a third of it or lowered by a seventh.
		switch strings.ToLower(strings.TrimSpace(rpr.VertAlign.Val)) {
		case "superscript":
			props.BaselineShiftPt += props.SizePt / 3
			props.SizePt *= superSubScale
		case "subscript":
			props.BaselineShiftPt -= props.SizePt / 7
			props.SizePt *= superSubScale
		}
	}
	return props
}

// superSubScale is how much smaller superscript and subscript text is drawn.
const superSubScale = 0.65

// resolveFontFamily resolves a w:rFonts into a typeface name: an explicit
// w:ascii/w:hAnsi wins, else the theme reference (w:asciiTheme/w:hAnsiTheme) is
// looked up in the theme's font scheme. "" when neither resolves, so the caller
// keeps its default.
func resolveFontFamily(f *xmlRFonts, themeFonts map[string]string) string {
	if f == nil {
		return ""
	}
	if name := firstNonEmpty(f.ASCII, f.HAnsi); name != "" {
		return name
	}
	for _, ref := range []string{f.ASCIITheme, f.HAnsiTheme} {
		if slot := themeFontSlot(ref); slot != "" {
			if name := themeFonts[slot]; name != "" {
				return name
			}
		}
	}
	return ""
}

// themeFontSlot maps a w:*Theme font reference ("minorHAnsi", "majorBidi", …)
// onto the font-scheme slot name used as the theme font map's key; "" for an
// unknown value.
func themeFontSlot(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var scheme string
	switch {
	case strings.HasPrefix(v, "major"):
		scheme = "major"
	case strings.HasPrefix(v, "minor"):
		scheme = "minor"
	default:
		return ""
	}
	switch strings.TrimPrefix(strings.TrimPrefix(v, "major"), "minor") {
	case "hansi", "ascii":
		return scheme + "latin"
	case "eastasia":
		return scheme + "ea"
	case "bidi":
		return scheme + "cs"
	default:
		return ""
	}
}

// resolveColor turns a w:color into "#RRGGBB" or "" (empty → backend default
// black). A theme reference (w:themeColor) is looked up in the theme map by its
// scheme slot and then lightened/darkened by its w:themeTint/w:themeShade; an
// unknown slot, "auto", or an absent value all resolve to "".
func resolveColor(c *xmlColor, theme map[string]string) string {
	if c == nil {
		return ""
	}
	return resolveThemeHex(c.Val, c.ThemeColor, c.ThemeTint, c.ThemeShade, theme)
}

// themeSlot maps a w:themeColor enum value onto the clrScheme slot name used as
// the theme map key; "" for a value with no mapped slot.
func themeSlot(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "dark1", "text1":
		return "dk1"
	case "light1", "background1":
		return "lt1"
	case "dark2", "text2":
		return "dk2"
	case "light2", "background2":
		return "lt2"
	case "accent1", "accent2", "accent3", "accent4", "accent5", "accent6":
		return strings.ToLower(v)
	case "hyperlink":
		return "hlink"
	case "followedhyperlink":
		return "folhlink"
	default:
		return ""
	}
}

func underlineOn(u *xmlVal) bool {
	if u == nil {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(u.Val), "none")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// buildTable converts a parsed <w:tbl> into the public Table model, resolving
// column widths and (per cell) merge/border/shading state. tableStyles is
// consulted when the table references a w:tblStyle; base and conditional
// region borders/shading fall under the table's own inline w:tblBorders
// (inline wins per side).
func buildTable(xt xmlTable, sc styleContext) Table {
	tc := tableContext{
		nRows: len(xt.Rows),
		nCols: tableGridColCount(xt),
	}
	var tblInd *xmlTblInd
	var look *xmlTblLook
	var inlineCellMar *xmlTcMar
	var fixedLayout bool
	if xt.Props != nil {
		tc.borders = xt.Props.Borders
		tblInd = xt.Props.Ind
		look = xt.Props.Look
		inlineCellMar = xt.Props.CellMar
		fixedLayout = xt.Props.Layout != nil &&
			strings.EqualFold(strings.TrimSpace(xt.Props.Layout.Type), "fixed")
		if xt.Props.StyleID != nil {
			if rs, ok := sc.tables[xt.Props.StyleID.Val]; ok {
				tc.style, tc.haveStyle = rs, true
				if tblInd == nil {
					tblInd = rs.Ind
				}
			}
		}
	}
	tc.flags = parseTblLook(look)
	// Cell margins: inline w:tblCellMar over the style's, per side; a cell's own
	// w:tcMar (resolved in buildCell) still wins over both.
	tc.cellMar = mergeTcMar(inlineCellMar, tc.style.CellMar)

	rows := make([]Row, 0, tc.nRows)
	headerRows := 0
	for ri, xr := range xt.Rows {
		row := buildRow(xr, tc, ri, sc)
		// Only leading header rows repeat: Word ignores a w:tblHeader further down.
		if row.Header && headerRows == ri {
			headerRows++
		}
		rows = append(rows, row)
	}
	widths, percents := resolveColumnWidths(xt)
	table := Table{
		Rows:           rows,
		ColumnWidths:   widths,
		ColumnPercents: percents,
		IndentPt:       tblIndPt(tblInd),
		HeaderRows:     headerRows,
		FixedLayout:    fixedLayout,
	}
	if xt.Props != nil {
		table.WidthPt, table.WidthPct = tblWidth(xt.Props.TblW)
		table.Float = tableFloatFrom(xt.Props.Float)
	}
	return table
}

// tblWidth reads a w:tblW as points (type dxa) or a percentage of the available
// width (type pct); an "auto" width is neither.
func tblWidth(w *xmlTblWidth) (pt, pct float64) {
	if w == nil {
		return 0, 0
	}
	return tblWidthValue(w)
}

// tableFloatFrom resolves <w:tblpPr> into the public placement (nil when the
// table is not floating). The text always wraps around a floating table.
func tableFloatFrom(p *xmlTblPPr) *Float {
	if p == nil {
		return nil
	}
	return &Float{
		XPt:          dxaToPt(p.X),
		YPt:          dxaToPt(p.Y),
		HorzAnchor:   anchorFrom(p.HorzAnchor),
		VertAnchor:   anchorFrom(p.VertAnchor),
		XSpec:        strings.ToLower(strings.TrimSpace(p.XSpec)),
		YSpec:        strings.ToLower(strings.TrimSpace(p.YSpec)),
		DistLeftPt:   max(dxaToPt(p.LeftFromText), 0),
		DistRightPt:  max(dxaToPt(p.RightFromText), 0),
		DistTopPt:    max(dxaToPt(p.TopFromText), 0),
		DistBottomPt: max(dxaToPt(p.BottomFromText), 0),
	}
}

// anchorFrom resolves w:horzAnchor/w:vertAnchor, or a picture's relativeFrom;
// the default (and anything unknown - column, character, paragraph, line)
// anchors to the text. The margin areas are approximated: those that start at
// the page's edge by the page, the others by the margin.
func anchorFrom(v string) Anchor {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "margin", "rightmargin", "bottommargin", "insidemargin", "outsidemargin":
		return AnchorMargin
	case "page", "leftmargin", "topmargin":
		return AnchorPage
	default:
		return AnchorText
	}
}

// tableContext is the table-wide state every cell needs to resolve its own
// formatting: the table's inline borders, its resolved named style (and whether
// it has one), the active conditional-region flags, the table-level cell margins,
// and the grid's size.
type tableContext struct {
	borders   *xmlBorders
	style     resolvedTableStyle
	haveStyle bool
	flags     tblLookFlags
	cellMar   *xmlTcMar
	nRows     int
	nCols     int
}

func tableGridColCount(xt xmlTable) int {
	if xt.Grid != nil && len(xt.Grid.Cols) > 0 {
		return len(xt.Grid.Cols)
	}
	max := 0
	for _, xr := range xt.Rows {
		n := 0
		for _, xc := range xr.Cells {
			n += gridSpan(xc.Props)
		}
		if n > max {
			max = n
		}
	}
	return max
}

func tblIndPt(ind *xmlTblInd) float64 {
	if ind == nil {
		return 0
	}
	return dxaToPt(ind.W)
}

func buildRow(xr xmlRow, tc tableContext, rowIdx int, sc styleContext) Row {
	cells := make([]Cell, 0, len(xr.Cells))
	colStart := 0
	for _, xc := range xr.Cells {
		cells = append(cells, buildCell(xc, tc, rowIdx, colStart, sc))
		colStart += gridSpan(xc.Props)
	}
	row := Row{Cells: cells}
	if xr.Props != nil {
		row.Header = xr.Props.TblHeader.on()
		row.HeightPt, row.HeightRule = rowHeightFrom(xr.Props.Height)
	}
	return row
}

// rowHeightFrom resolves w:trHeight into a height and its rule. An absent
// height, an unparseable one, or w:hRule="auto" all leave the row auto-sized.
func rowHeightFrom(h *xmlTrHeight) (float64, RowHeightRule) {
	if h == nil {
		return 0, RowHeightAuto
	}
	pt := dxaToPt(h.Val)
	if pt <= 0 {
		return 0, RowHeightAuto
	}
	switch strings.ToLower(strings.TrimSpace(h.HRule)) {
	case "exact":
		return pt, RowHeightExact
	case "atleast", "": // an absent rule with a height behaves as a minimum
		return pt, RowHeightAtLeast
	default: // "auto"
		return 0, RowHeightAuto
	}
}

func buildCell(xc xmlCell, tc tableContext, rowIdx, colStart int, sc styleContext) Cell {
	var contrib tableStyleContribution
	if tc.haveStyle {
		contrib = styleContributionForCell(tc, rowIdx, colStart)
	}
	// The table style's paragraph/run formatting is a tier of its own, under the
	// paragraph and character styles but over w:docDefaults - so a style whose
	// firstRow region is bold reaches this cell's runs without overriding a
	// paragraph that says otherwise.
	cellSC := sc
	cellSC.tablePPr = contrib.PPr
	cellSC.tableRPr = contrib.RPr

	span := gridSpan(xc.Props)
	// Table-level borders (inline over the style's) decide a cell's sides by its
	// position; the style's cell-level borders land on it whatever its position,
	// under its own inline w:tcBorders.
	tblBorders := mergeBorders(tc.borders, contrib.TblBorders)
	cellBorders := mergeBorders(tcBordersOf(xc.Props), contrib.CellBorders)
	pos := cellPosition{
		firstRow: rowIdx == 0,
		lastRow:  rowIdx == tc.nRows-1,
		firstCol: colStart == 0,
		lastCol:  colStart+span >= tc.nCols,
	}
	content, _ := buildBody(xmlBody{Children: xc.Children}, cellSC)
	cell := Cell{
		Content:       content,
		ColSpan:       span,
		VMerge:        vMergeState(xc.Props),
		Borders:       resolveCellBorders(tblBorders, cellBorders, pos),
		Shading:       resolveShading(coalesceShd(shdOf(xc.Props), contrib.Shd), sc.theme),
		Margins:       resolveCellMargins(mergeTcMar(tcMarOf(xc.Props), tc.cellMar)),
		TextDirection: textDirectionFrom(textDirOf(xc.Props)),
		VAlign:        vAlignFrom(vAlignOf(xc.Props)),
	}
	for _, el := range content {
		switch {
		case el.Paragraph != nil:
			cell.Paragraphs = append(cell.Paragraphs, *el.Paragraph)
		case el.Table != nil && cell.Nested == nil:
			cell.Nested = el.Table
		}
	}
	return cell
}

// tableStyleContribution is what a table style gives one cell: its table-level
// borders (whose inside grid lines still only apply between cells) and its
// cell-level ones (which land on the cell whatever its position), its shading,
// and the paragraph and run formatting of the regions covering it.
type tableStyleContribution struct {
	TblBorders  *xmlBorders
	CellBorders *xmlBorders
	Shd         *xmlShd
	PPr         *xmlPPr
	RPr         *xmlRPr
}

// styleContributionForCell layers the style's base and conditional regions for
// one cell. Priority (low→high): base → whole table → h-band → v-band →
// first/last col → first/last row → the corner cell, which Word lets win over
// every other region.
func styleContributionForCell(tc tableContext, rowIdx, colStart int) tableStyleContribution {
	style, flags, nRows, nCols := tc.style, tc.flags, tc.nRows, tc.nCols
	out := tableStyleContribution{
		TblBorders:  style.Borders,
		CellBorders: style.CellBorders,
		Shd:         style.Shd,
		PPr:         style.PPr,
		RPr:         style.RPr,
	}

	apply := func(typ string) {
		r, ok := style.Regions[typ]
		if !ok {
			return
		}
		out.TblBorders = mergeBorders(r.TblBorders, out.TblBorders)
		out.CellBorders = mergeBorders(r.CellBorders, out.CellBorders)
		out.Shd = coalesceShd(r.Shd, out.Shd)
		out.PPr = mergePPr(r.PPr, out.PPr)
		out.RPr = mergeRPr(r.RPr, out.RPr)
	}

	apply("wholeTable")
	if !flags.NoHBand {
		if band := horzBandType(rowIdx, nRows, flags, style.RowBandSize); band != "" {
			apply(band)
		}
	}
	if !flags.NoVBand {
		if band := vertBandType(colStart, nCols, flags, style.ColBandSize); band != "" {
			apply(band)
		}
	}
	firstCol := flags.FirstCol && colStart == 0
	lastCol := flags.LastCol && nCols > 0 && colStart == nCols-1
	firstRow := flags.FirstRow && rowIdx == 0
	lastRow := flags.LastRow && nRows > 0 && rowIdx == nRows-1
	if firstCol {
		apply("firstCol")
	}
	if lastCol {
		apply("lastCol")
	}
	if firstRow {
		apply("firstRow")
	}
	if lastRow {
		apply("lastRow")
	}
	switch {
	case firstRow && firstCol:
		apply("nwCell")
	case firstRow && lastCol:
		apply("neCell")
	case lastRow && firstCol:
		apply("swCell")
	case lastRow && lastCol:
		apply("seCell")
	}
	return out
}

// defaultCellMargins are Word's own: 108 dxa (5.4 pt) left and right, none top
// and bottom.
var defaultCellMargins = CellMargins{LeftPt: 5.4, RightPt: 5.4}

// resolveCellMargins turns an already-merged w:tcMar/w:tblCellMar into margins,
// falling back per side to Word's defaults. A side declared as a percentage
// keeps its percentage: it is resolved against the table's width at layout time.
func resolveCellMargins(m *xmlTcMar) CellMargins {
	out := defaultCellMargins
	if m == nil {
		return out
	}
	side := func(w *xmlTblWidth, defPt float64) (float64, float64) {
		if w == nil {
			return defPt, 0
		}
		pt, pct := tblWidthValue(w)
		if pct > 0 {
			return 0, pct
		}
		if strings.TrimSpace(w.W) == "" { // a side with no width at all keeps the default
			return defPt, 0
		}
		return pt, 0
	}
	out.TopPt, out.TopPct = side(m.Top, out.TopPt)
	out.BottomPt, out.BottomPct = side(m.Bottom, out.BottomPt)
	out.LeftPt, out.LeftPct = side(m.Left, out.LeftPt)
	out.RightPt, out.RightPct = side(m.Right, out.RightPt)
	return out
}

// textDirectionFrom resolves w:textDirection; anything but the two vertical
// modes (including the default "lrTb") is horizontal.
func textDirectionFrom(v *xmlVal) TextDirection {
	if v == nil {
		return TextDirectionHorizontal
	}
	switch strings.ToLower(strings.TrimSpace(v.Val)) {
	case "btlr", "blrt": // bottom-to-top
		return TextDirectionBTLR
	case "tbrl", "tbrlv", "tbv": // top-to-bottom
		return TextDirectionTBRL
	default:
		return TextDirectionHorizontal
	}
}

func tcMarOf(tcPr *xmlTcPr) *xmlTcMar {
	if tcPr == nil {
		return nil
	}
	return tcPr.Mar
}

func textDirOf(tcPr *xmlTcPr) *xmlVal {
	if tcPr == nil {
		return nil
	}
	return tcPr.TextDir
}

func horzBandType(rowIdx, nRows int, flags tblLookFlags, bandSize int) string {
	bodyStart, bodyEnd := 0, nRows
	if flags.FirstRow {
		bodyStart = 1
	}
	if flags.LastRow {
		bodyEnd = nRows - 1
	}
	if rowIdx < bodyStart || rowIdx >= bodyEnd {
		return ""
	}
	if bandSize < 1 {
		bandSize = 1
	}
	band := ((rowIdx - bodyStart) / bandSize) % 2
	if band == 0 {
		return "band1Horz"
	}
	return "band2Horz"
}

func vertBandType(colStart, nCols int, flags tblLookFlags, bandSize int) string {
	bodyStart, bodyEnd := 0, nCols
	if flags.FirstCol {
		bodyStart = 1
	}
	if flags.LastCol {
		bodyEnd = nCols - 1
	}
	if colStart < bodyStart || colStart >= bodyEnd {
		return ""
	}
	if bandSize < 1 {
		bandSize = 1
	}
	band := ((colStart - bodyStart) / bandSize) % 2
	if band == 0 {
		return "band1Vert"
	}
	return "band2Vert"
}

// maxGridColumns is Word's own limit on a table's columns. A cell's w:gridSpan
// is clamped to it: the column-width arrays grow to cover every span, so an
// absurd span in an untrusted file would otherwise allocate without bound.
const maxGridColumns = 63

func gridSpan(tcPr *xmlTcPr) int {
	if tcPr == nil || tcPr.GridSpan == nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(tcPr.GridSpan.Val))
	if err != nil || n < 1 {
		return 1
	}
	return min(n, maxGridColumns)
}

func vMergeState(tcPr *xmlTcPr) VMergeState {
	if tcPr == nil || tcPr.VMerge == nil {
		return VMergeNone
	}
	if strings.EqualFold(strings.TrimSpace(tcPr.VMerge.Val), "restart") {
		return VMergeRestart
	}
	return VMergeContinue // absent w:val (self-closing) or explicit "continue"
}

func tcBordersOf(tcPr *xmlTcPr) *xmlBorders {
	if tcPr == nil {
		return nil
	}
	return tcPr.Borders
}

func shdOf(tcPr *xmlTcPr) *xmlShd {
	if tcPr == nil {
		return nil
	}
	return tcPr.Shd
}

// cellPosition is where a cell sits in the grid, which decides whether a side of
// it takes the table's outer border or its inside grid line.
type cellPosition struct {
	firstRow, lastRow, firstCol, lastCol bool
}

// resolveCellBorders resolves each side from the cell's own w:tcBorders over the
// table's: an edge of the table takes the matching outer border (w:top, w:left,
// …), an edge between two cells takes the inside grid line (w:insideH/w:insideV).
// The two diagonals come from the cell alone: a table has no diagonal to inherit.
func resolveCellBorders(tblBorders, tcBorders *xmlBorders, pos cellPosition) CellBorders {
	var tbl, cell xmlBorders
	if tblBorders != nil {
		tbl = *tblBorders
	}
	if tcBorders != nil {
		cell = *tcBorders
	}
	outerOrInside := func(outer, inside *xmlBorder, atEdge bool) *xmlBorder {
		if atEdge {
			return outer
		}
		return inside
	}
	return CellBorders{
		Top:      resolveBorderSide(outerOrInside(tbl.Top, tbl.InsideH, pos.firstRow), cell.Top),
		Bottom:   resolveBorderSide(outerOrInside(tbl.Bottom, tbl.InsideH, pos.lastRow), cell.Bottom),
		Left:     resolveBorderSide(outerOrInside(tbl.Left, tbl.InsideV, pos.firstCol), cell.Left),
		Right:    resolveBorderSide(outerOrInside(tbl.Right, tbl.InsideV, pos.lastCol), cell.Right),
		DiagDown: resolveBorderSide(nil, cell.TL2BR),
		DiagUp:   resolveBorderSide(nil, cell.TR2BL),
	}
}

// vAlignFrom resolves w:vAlign; anything but center/bottom (including the
// default "top") puts the content at the cell's top.
func vAlignFrom(v *xmlVal) VerticalAlign {
	if v == nil {
		return VAlignTop
	}
	switch strings.ToLower(strings.TrimSpace(v.Val)) {
	case "center":
		return VAlignCenter
	case "bottom":
		return VAlignBottom
	default:
		return VAlignTop
	}
}

func vAlignOf(tcPr *xmlTcPr) *xmlVal {
	if tcPr == nil {
		return nil
	}
	return tcPr.VAlign
}

// defaultBorderWidthPt is used when a declared border omits w:sz (or it's
// unparseable), matching Word's own thin default rule width.
const defaultBorderWidthPt = 0.75

// resolveBorderSide prefers the cell-level border, falling back to the
// table-level one; a nil result (neither declared) is the zero BorderSide,
// meaning no border. Unlike shading, a border's color defaults to black
// ("auto"/absent) rather than "no color" - the border still renders.
func resolveBorderSide(tblSide, cellSide *xmlBorder) BorderSide {
	b := cellSide
	if b == nil {
		b = tblSide
	}
	if b == nil {
		return BorderSide{}
	}
	// "nil" is how a cell removes a border it would otherwise inherit.
	switch strings.ToLower(strings.TrimSpace(b.Val)) {
	case "none", "nil":
		return BorderSide{}
	}
	width := eighthsToPt(b.Sz) // border w:sz is in eighths of a point
	if width <= 0 {
		width = defaultBorderWidthPt
	}
	return BorderSide{
		Style:   b.Val,
		WidthPt: width,
		Color:   normalizeBorderColor(b.Color),
	}
}

// normalizeBorderColor turns a raw OOXML border color into "#RRGGBB",
// defaulting "auto", an absent, or an unusable value to black - a border with a
// declared style still needs a visible color.
func normalizeBorderColor(v string) string {
	if hex, ok := sRGBHex(v); ok {
		return hex
	}
	return "#000000"
}

// resolveColumnWidths reads column widths from w:tblGrid/w:gridCol; a column the
// grid does not size (or does not mention at all) falls back to the widest
// single-span w:tcW seen for it - in points when that w:tcW is a dxa width, or
// as a percentage of the available width when it is a "pct" one (cells
// contributing via gridSpan aren't attributable to one column, so they don't
// affect the fallback). A column with neither is left at zero: the layout sizes
// it from its content.
func resolveColumnWidths(xt xmlTable) (widths, percents []float64) {
	if xt.Grid != nil && len(xt.Grid.Cols) > 0 {
		widths = make([]float64, len(xt.Grid.Cols))
		for i, c := range xt.Grid.Cols {
			widths[i] = dxaToPt(c.W)
		}
	}
	percents = make([]float64, len(widths))

	for _, row := range xt.Rows {
		col := 0
		for _, cell := range row.Cells {
			span := gridSpan(cell.Props)
			for len(widths) < col+span {
				widths = append(widths, 0)
				percents = append(percents, 0)
			}
			if span == 1 && widths[col] == 0 {
				pt, pct := cellWidth(cell.Props)
				widths[col] = math.Max(widths[col], pt)
				percents[col] = math.Max(percents[col], pct)
			}
			col += span
		}
	}
	return widths, percents
}

// cellWidth reads a cell's w:tcW; see tblWidthValue.
func cellWidth(tcPr *xmlTcPr) (pt, pct float64) {
	if tcPr == nil {
		return 0, 0
	}
	return tblWidthValue(tcPr.TcW)
}

// tblWidthValue reads a w:w/w:type pair (w:tcW, w:tblW, a w:tcMar side) as
// either a width in points (type dxa, the default) or a percentage of the
// reference width (type pct, in fiftieths of a percent, or written with a
// trailing %). An "auto" width is neither.
func tblWidthValue(w *xmlTblWidth) (pt, pct float64) {
	if w == nil {
		return 0, 0
	}
	v := strings.TrimSpace(w.W)
	switch strings.ToLower(strings.TrimSpace(w.Type)) {
	case "", "dxa":
		return dxaToPt(v), 0
	case "pct":
		if s, ok := strings.CutSuffix(v, "%"); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil || f <= 0 {
				return 0, 0
			}
			return 0, f
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			return 0, 0
		}
		return 0, f / 50 // fiftieths of a percent
	default: // "auto", "nil"
		return 0, 0
	}
}

// dxaToPt converts twentieths of a point (the unit w:w/w:tblGrid use) to points.
func dxaToPt(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v / 20
}

// dxaToPtOr converts a dxa attribute to points, returning def when the
// attribute is absent (empty) or unparseable. Unlike dxaToPt, a declared "0"
// is honored as 0 pt (a valid margin), so only truly missing values default.
func dxaToPtOr(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v / 20
}

// Default page geometry (A4, one-inch margins) used when a document declares
// no w:sectPr, no w:pgSz/w:pgMar, or an unparseable value.
const (
	defaultPageWidthPt  = 595.28 // A4 width
	defaultPageHeightPt = 841.89 // A4 height
	defaultMarginPt     = 72.0   // 1 inch
)

// buildPageGeometry resolves a body-level w:sectPr into the public
// PageGeometry, converting dxa → pt and falling back to the A4/one-inch
// defaults per missing element or attribute (nil-safe). A non-positive page
// dimension is treated as absent.
func buildPageGeometry(sp *xmlSectPr) PageGeometry {
	g := PageGeometry{
		WidthPt:        defaultPageWidthPt,
		HeightPt:       defaultPageHeightPt,
		MarginTopPt:    defaultMarginPt,
		MarginRightPt:  defaultMarginPt,
		MarginBottomPt: defaultMarginPt,
		MarginLeftPt:   defaultMarginPt,
	}
	if sp == nil {
		return g
	}
	if sp.PgSz != nil {
		if w := dxaToPtOr(sp.PgSz.W, 0); w > 0 {
			g.WidthPt = w
		}
		if h := dxaToPtOr(sp.PgSz.H, 0); h > 0 {
			g.HeightPt = h
		}
	}
	if sp.PgMar != nil {
		// A negative top/bottom margin only says the header/footer may not push
		// the text; the text still starts at its absolute value.
		g.MarginTopPt = math.Abs(dxaToPtOr(sp.PgMar.Top, defaultMarginPt))
		g.MarginRightPt = dxaToPtOr(sp.PgMar.Right, defaultMarginPt)
		g.MarginBottomPt = math.Abs(dxaToPtOr(sp.PgMar.Bottom, defaultMarginPt))
		g.MarginLeftPt = dxaToPtOr(sp.PgMar.Left, defaultMarginPt)
	}
	return g
}

// eighthsToPt converts eighths of a point (the unit border w:sz uses) to points.
func eighthsToPt(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v / 8
}

// --- Theme colors (word/theme/theme1.xml) ---
//
// theme1.xml is a DrawingML part (its own namespace), so its elements do not
// match the WordprocessingML tags used elsewhere. Only the color scheme is
// modeled: each named slot maps to an sRGB hex used to resolve w:themeColor.

type xmlTheme struct {
	Elements xmlThemeElements `xml:"http://schemas.openxmlformats.org/drawingml/2006/main themeElements"`
}

type xmlThemeElements struct {
	ClrScheme  xmlClrScheme  `xml:"http://schemas.openxmlformats.org/drawingml/2006/main clrScheme"`
	FontScheme xmlFontScheme `xml:"http://schemas.openxmlformats.org/drawingml/2006/main fontScheme"`
}

// xmlFontScheme is <a:fontScheme>: the major (headings) and minor (body) font
// collections a w:rFonts theme reference resolves against.
type xmlFontScheme struct {
	Major xmlFontCollection `xml:"http://schemas.openxmlformats.org/drawingml/2006/main majorFont"`
	Minor xmlFontCollection `xml:"http://schemas.openxmlformats.org/drawingml/2006/main minorFont"`
}

// xmlFontCollection is one font collection's Latin, East-Asian and
// complex-script typefaces.
type xmlFontCollection struct {
	Latin xmlTypeface `xml:"http://schemas.openxmlformats.org/drawingml/2006/main latin"`
	EA    xmlTypeface `xml:"http://schemas.openxmlformats.org/drawingml/2006/main ea"`
	CS    xmlTypeface `xml:"http://schemas.openxmlformats.org/drawingml/2006/main cs"`
}

type xmlTypeface struct {
	Typeface string `xml:"typeface,attr"`
}

// xmlClrScheme models the twelve named color-scheme slots. dk1/lt1 typically
// carry a sysClr (windowText/window) with a lastClr fallback; the rest carry an
// srgbClr.
type xmlClrScheme struct {
	Dk1      xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main dk1"`
	Lt1      xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main lt1"`
	Dk2      xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main dk2"`
	Lt2      xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main lt2"`
	Accent1  xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main accent1"`
	Accent2  xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main accent2"`
	Accent3  xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main accent3"`
	Accent4  xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main accent4"`
	Accent5  xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main accent5"`
	Accent6  xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main accent6"`
	Hlink    xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main hlink"`
	FolHlink xmlThemeColor `xml:"http://schemas.openxmlformats.org/drawingml/2006/main folHlink"`
}

type xmlThemeColor struct {
	SrgbClr *xmlSrgbClr `xml:"http://schemas.openxmlformats.org/drawingml/2006/main srgbClr"`
	SysClr  *xmlSysClr  `xml:"http://schemas.openxmlformats.org/drawingml/2006/main sysClr"`
}

type xmlSrgbClr struct {
	Val string `xml:"val,attr"`
}

type xmlSysClr struct {
	LastClr string `xml:"lastClr,attr"`
}

// hex returns the slot's "#RRGGBB" value (srgbClr wins over sysClr's lastClr),
// or "" when neither carries a usable value.
func (tc xmlThemeColor) hex() string {
	if tc.SrgbClr != nil {
		if v := strings.TrimSpace(tc.SrgbClr.Val); v != "" {
			return "#" + strings.ToUpper(v)
		}
	}
	if tc.SysClr != nil {
		if v := strings.TrimSpace(tc.SysClr.LastClr); v != "" {
			return "#" + strings.ToUpper(v)
		}
	}
	return ""
}

// buildThemeMap turns a parsed theme into a slot→"#RRGGBB" map (empty when the
// theme is nil). Slots with no usable color are omitted, so an unresolved
// w:themeColor reference falls through to "".
func buildThemeMap(t *xmlTheme) map[string]string {
	m := make(map[string]string)
	if t == nil {
		return m
	}
	cs := t.Elements.ClrScheme
	for slot, tc := range map[string]xmlThemeColor{
		"dk1": cs.Dk1, "lt1": cs.Lt1, "dk2": cs.Dk2, "lt2": cs.Lt2,
		"accent1": cs.Accent1, "accent2": cs.Accent2, "accent3": cs.Accent3,
		"accent4": cs.Accent4, "accent5": cs.Accent5, "accent6": cs.Accent6,
		"hlink": cs.Hlink, "folhlink": cs.FolHlink,
	} {
		if hex := tc.hex(); hex != "" {
			m[slot] = hex
		}
	}
	return m
}

// buildThemeFontMap turns a parsed theme's font scheme into a slot→typeface map
// ("majorlatin", "minorea", …), the keys a w:rFonts theme reference resolves to.
// Slots with no declared typeface are omitted, so an unresolved reference falls
// back to the default font.
func buildThemeFontMap(t *xmlTheme) map[string]string {
	m := make(map[string]string)
	if t == nil {
		return m
	}
	fs := t.Elements.FontScheme
	for slot, tf := range map[string]xmlTypeface{
		"majorlatin": fs.Major.Latin, "majorea": fs.Major.EA, "majorcs": fs.Major.CS,
		"minorlatin": fs.Minor.Latin, "minorea": fs.Minor.EA, "minorcs": fs.Minor.CS,
	} {
		if name := strings.TrimSpace(tf.Typeface); name != "" {
			m[slot] = name
		}
	}
	return m
}

// --- List numbering (word/numbering.xml) ---

type xmlNumbering struct {
	PicBullets   []xmlNumPicBullet `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main numPicBullet"`
	AbstractNums []xmlAbstractNum  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main abstractNum"`
	Nums         []xmlNum          `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main num"`
}

// xmlNumPicBullet is <w:numPicBullet>: an image a bullet level can use as its
// marker, referenced by w:lvlPicBulletId. It is always a VML picture, even in
// files Word writes today.
type xmlNumPicBullet struct {
	ID   string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main numPicBulletId,attr"`
	Pict *xmlPict `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pict"`
}

type xmlAbstractNum struct {
	AbstractNumID string   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main abstractNumId,attr"`
	Levels        []xmlLvl `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lvl"`
}

type xmlLvl struct {
	Ilvl        string    `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main ilvl,attr"`
	Start       *xmlVal   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main start"`
	NumFmt      *xmlVal   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main numFmt"`
	LvlText     *xmlVal   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lvlText"`
	IsLgl       *xmlOnOff `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main isLgl"`
	Suff        *xmlVal   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main suff"`
	PicBulletID *xmlVal   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lvlPicBulletId"`
	PPr         *xmlPPr   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main pPr"`
	RPr         *xmlRPr   `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main rPr"`
}

type xmlNum struct {
	NumID         string           `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main numId,attr"`
	AbstractNumID *xmlVal          `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main abstractNumId"`
	LvlOverrides  []xmlLvlOverride `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lvlOverride"`
}

type xmlLvlOverride struct {
	Ilvl          string  `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main ilvl,attr"`
	StartOverride *xmlVal `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main startOverride"`
	Lvl           *xmlLvl `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main lvl"`
}

// numLevel is one resolved list level: its start value, number format, the
// lvlText template (with %1..%9 placeholders), whether it renders every
// referenced level as a decimal (w:isLgl, "legal" numbering), what separates the
// marker from the text (w:suff), the level's hanging indent, the marker run's
// rPr, and - for a picture bullet - the marker image.
type numLevel struct {
	start   int
	numFmt  string
	lvlText string
	isLgl   bool
	suff    string
	ind     *xmlInd
	rpr     *xmlRPr
	image   *Image
}

// numDef is a resolved list (w:num): its levels keyed by ilvl.
type numDef struct {
	levels map[int]numLevel
}

// marker is a resolved list marker returned by numberingState.advance: the
// formatted text (or a picture bullet image), the level's indent, the marker
// run's properties, and the suffix separating it from the paragraph's text.
type marker struct {
	text  string
	image *Image
	suff  string
	ind   *xmlInd
	rpr   *xmlRPr
}

// numberingState holds the static list definitions plus the mutable
// document-order counters (per numId, per ilvl). It is threaded by pointer
// through the builders so paragraphs - including those inside table cells -
// advance one shared counter in document order.
type numberingState struct {
	defs     map[string]numDef
	counters map[string]map[int]int
	used     map[string]map[int]bool
}

// newNumberingState returns a fresh counter state over defs, or nil when there
// are no lists (so buildParagraph skips numbering entirely).
func newNumberingState(defs map[string]numDef) *numberingState {
	if len(defs) == 0 {
		return nil
	}
	return &numberingState{
		defs:     defs,
		counters: make(map[string]map[int]int),
		used:     make(map[string]map[int]bool),
	}
}

// fork returns a state over the same list definitions with its own counters,
// used for header/footer parts: a numbered list there is independent of the
// body's, so building one cannot advance the body's counters (nil-safe).
func (ns *numberingState) fork() *numberingState {
	if ns == nil {
		return nil
	}
	return newNumberingState(ns.defs)
}

// advance updates numID's counter for a paragraph at level ilvl and returns the
// formatted marker. First use of a level starts it at the level's start value;
// reuse increments by one; deeper levels reset; shallower levels are unchanged.
// ok is false when numID or ilvl is unknown.
func (ns *numberingState) advance(numID string, ilvl int) (marker, bool) {
	def, ok := ns.defs[numID]
	if !ok {
		return marker{}, false
	}
	lvl, ok := def.levels[ilvl]
	if !ok {
		return marker{}, false
	}
	if ns.counters[numID] == nil {
		ns.counters[numID] = make(map[int]int)
		ns.used[numID] = make(map[int]bool)
	}
	if ns.used[numID][ilvl] {
		ns.counters[numID][ilvl]++
	} else {
		ns.counters[numID][ilvl] = lvl.start
		ns.used[numID][ilvl] = true
	}
	for l := range ns.used[numID] {
		if l > ilvl {
			delete(ns.used[numID], l)
			delete(ns.counters[numID], l)
		}
	}
	return marker{
		text:  formatMarker(def, ilvl, ns.counters[numID]),
		image: lvl.image,
		suff:  lvl.suff,
		ind:   lvl.ind,
		rpr:   lvl.rpr,
	}, true
}

// buildNumbering resolves word/numbering.xml into numId→numDef: each num's
// abstractNum levels, with lvlOverride start/fmt applied on top (nil-safe).
// picBullets resolves a level's w:lvlPicBulletId into its marker image; it is
// built from the numbering part's own relationships, which is where a picture
// bullet's media reference lives.
func buildNumbering(xn *xmlNumbering, ir *imageResolver) map[string]numDef {
	out := make(map[string]numDef)
	if xn == nil {
		return out
	}
	bullets := make(map[string]*Image)
	for _, pb := range xn.PicBullets {
		if img := pb.Pict.image(ir); img != nil {
			bullets[strings.TrimSpace(pb.ID)] = img
		}
	}
	abstracts := make(map[string]xmlAbstractNum)
	for _, a := range xn.AbstractNums {
		abstracts[strings.TrimSpace(a.AbstractNumID)] = a
	}
	for _, num := range xn.Nums {
		numID := strings.TrimSpace(num.NumID)
		if numID == "" || num.AbstractNumID == nil {
			continue
		}
		a, ok := abstracts[strings.TrimSpace(num.AbstractNumID.Val)]
		if !ok {
			continue
		}
		levels := make(map[int]numLevel)
		for _, l := range a.Levels {
			if idx, lv, ok := parseLevel(l, bullets); ok {
				levels[idx] = lv
			}
		}
		for _, ov := range num.LvlOverrides {
			idx, err := strconv.Atoi(strings.TrimSpace(ov.Ilvl))
			if err != nil {
				continue
			}
			lv := levels[idx] // zero value when the abstract lacked this level
			if ov.Lvl != nil {
				if _, plv, ok := parseLevel(*ov.Lvl, bullets); ok {
					lv = plv
				}
			}
			if ov.StartOverride != nil {
				if s, err := strconv.Atoi(strings.TrimSpace(ov.StartOverride.Val)); err == nil {
					lv.start = s
				}
			}
			levels[idx] = lv
		}
		out[numID] = numDef{levels: levels}
	}
	return out
}

// parseLevel resolves one w:lvl; ok is false when its w:ilvl is missing or
// unparseable. Defaults: start 1, format decimal, suffix tab.
func parseLevel(l xmlLvl, bullets map[string]*Image) (int, numLevel, bool) {
	idx, err := strconv.Atoi(strings.TrimSpace(l.Ilvl))
	if err != nil {
		return 0, numLevel{}, false
	}
	lv := numLevel{start: 1, numFmt: "decimal", suff: "tab"}
	if l.Start != nil {
		if s, err := strconv.Atoi(strings.TrimSpace(l.Start.Val)); err == nil {
			lv.start = s
		}
	}
	if l.NumFmt != nil {
		lv.numFmt = strings.TrimSpace(l.NumFmt.Val)
	}
	if l.LvlText != nil {
		lv.lvlText = l.LvlText.Val
	}
	if l.Suff != nil {
		if s := strings.TrimSpace(l.Suff.Val); s != "" {
			lv.suff = s
		}
	}
	lv.isLgl = l.IsLgl.on()
	if l.PicBulletID != nil {
		lv.image = bullets[strings.TrimSpace(l.PicBulletID.Val)]
	}
	if l.RPr != nil {
		lv.rpr = l.RPr
	}
	if l.PPr != nil {
		lv.ind = l.PPr.Ind
	}
	return idx, lv, true
}

// formatMarker substitutes %1..%9 in the level's lvlText with the counter
// value of each referenced level, formatted per that level's numFmt. A bullet
// level renders its literal lvlText (or "•" when empty, and nothing at all when
// a picture bullet takes its place); an unknown format behaves as decimal. A
// legal-numbering level (w:isLgl) renders every referenced level as a decimal,
// however those levels are formatted themselves - so "Article I" numbering
// becomes "1.1" inside a legal sub-level.
func formatMarker(def numDef, ilvl int, counters map[int]int) string {
	lvl := def.levels[ilvl]
	if lvl.image != nil {
		return ""
	}
	if strings.EqualFold(lvl.numFmt, "bullet") {
		if lvl.lvlText == "" {
			return "•"
		}
		return lvl.lvlText
	}
	s := lvl.lvlText
	for k := 0; k <= min(ilvl, 8); k++ { // lvlText has only %1..%9
		placeholder := "%" + strconv.Itoa(k+1)
		if !strings.Contains(s, placeholder) {
			continue
		}
		fmtName := "decimal"
		if kl, ok := def.levels[k]; ok && !lvl.isLgl {
			fmtName = kl.numFmt
		}
		s = strings.ReplaceAll(s, placeholder, formatNumber(counters[k], fmtName))
	}
	return s
}

// formatNumber renders n per an OOXML w:numFmt; an unrecognized format falls
// back to decimal.
func formatNumber(n int, numFmt string) string {
	switch strings.ToLower(strings.TrimSpace(numFmt)) {
	case "lowerletter":
		return toLetter(n, 'a')
	case "upperletter":
		return toLetter(n, 'A')
	case "lowerroman":
		return strings.ToLower(toRoman(n))
	case "upperroman":
		return toRoman(n)
	default: // "decimal" and anything unknown
		return strconv.Itoa(n)
	}
}

// maxLetterNumber is the last value Word renders as letters (30 repetitions of
// z); beyond it - and for a hostile w:start - the repeated-letter form would
// grow without bound.
const maxLetterNumber = 26 * 30

// toLetter renders n (1-based) the way Word letters a list, repeating the letter
// past z from base ('a' or 'A'): 1→a, 26→z, 27→aa, 28→bb. n<1 yields "", and n
// past maxLetterNumber falls back to decimal.
func toLetter(n int, base rune) string {
	if n < 1 {
		return ""
	}
	if n > maxLetterNumber {
		return strconv.Itoa(n)
	}
	return strings.Repeat(string(base+rune((n-1)%26)), (n-1)/26+1)
}

// toRoman renders n as an uppercase Roman numeral; n<=0, and n past 3999 (where
// the numerals would need an overline), fall back to decimal.
func toRoman(n int) string {
	if n <= 0 || n > 3999 {
		return strconv.Itoa(n)
	}
	vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	syms := []string{"M", "CM", "D", "CD", "C", "XC", "L", "XL", "X", "IX", "V", "IV", "I"}
	var b strings.Builder
	for i, v := range vals {
		for n >= v {
			b.WriteString(syms[i])
			n -= v
		}
	}
	return b.String()
}
