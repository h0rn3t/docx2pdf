package document

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"
	"golang.org/x/image/font/sfnt"

	"github.com/h0rn3t/docx2pdf/document/fonts"
)

// fpdfRenderer implements renderer on top of github.com/go-pdf/fpdf, drawing
// text through the embedded Liberation Unicode fonts (see document/fonts) so
// non-Latin scripts render as correct glyphs instead of PDF-core-font mojibake.
type fpdfRenderer struct {
	pdf *fpdf.Fpdf
	// images records, per Image.Name, whether the picture is registered with
	// the backend (true) or was rejected by it (false), so a picture drawn on
	// many pages is embedded - or turned down - once.
	images map[string]bool
	// anchors maps a bookmark name to the backend's internal link id; placed
	// records which of them Anchor has given a destination.
	anchors map[string]int
	placed  map[string]bool
	// faces are the real fonts - embedded in the document, or installed -
	// registered with the backend, by face; a nil entry remembers a face that
	// was looked for and is not available.
	faces map[faceKey]*realFace
	// system finds installed fonts, or is nil when they are not used.
	system *fontIndex
	// cur is what the last SetFont asked for; the face it resolves to is
	// picked per string, since a real font may lack a glyph the string needs.
	cur    fontRequest
	glyphs sfnt.Buffer
}

// realFace is a real font registered with the backend under name, with its
// parsed outlines to check which characters it has.
type realFace struct {
	name string
	font *sfnt.Font
}

type fontRequest struct {
	family                          string
	bold, italic, underline, strike bool
	size                            float64
}

// utf8Styles are the style strings each embedded family is registered under,
// matching fpdf's AddUTF8Font convention ("" regular, B, I, BI).
var utf8Styles = []struct {
	str          string
	bold, italic bool
}{
	{"", false, false},
	{"B", true, false},
	{"I", false, true},
	{"BI", true, true},
}

// newFPDFRenderer creates a backend whose pages are widthPt × heightPt (points),
// sized from the document's page geometry rather than a fixed A4. Text in a
// family the document embeds (embedded) is drawn in it, and - when system is
// not nil - text in a family installed on the machine in that font; anything
// else in a metric-compatible replacement.
// simplified: no SetMargins - auto page breaks are off and the layout positions
// all text by absolute coordinates, so fpdf's own margins have no effect here.
func newFPDFRenderer(widthPt, heightPt float64, embedded []Font, system *fontIndex) *fpdfRenderer {
	pdf := fpdf.NewCustom(&fpdf.InitType{
		UnitStr: "pt",
		Size:    fpdf.SizeType{Wd: widthPt, Ht: heightPt},
	})
	pdf.SetAutoPageBreak(false, 0) // pagination is handled by the layout
	for _, family := range fonts.Families {
		for _, s := range utf8Styles {
			pdf.AddUTF8FontFromBytes(family, s.str, fonts.Bytes(family, s.bold, s.italic))
		}
	}
	pdf.SetFont(fonts.Sans, "", defaultRenderSizePt) // a font must be set before any text op
	r := &fpdfRenderer{
		pdf: pdf, images: make(map[string]bool), anchors: make(map[string]int), placed: make(map[string]bool),
		faces: make(map[faceKey]*realFace), system: system,
		cur: fontRequest{family: fonts.Sans, size: defaultRenderSizePt},
	}
	for _, f := range embedded {
		if k := newFaceKey(f.Family, f.Bold, f.Italic); r.faces[k] == nil {
			r.faces[k] = r.register(f.Data)
		}
	}
	return r
}

// register adds a real font to the document, or returns nil when the backend
// cannot use it.
func (r *fpdfRenderer) register(data []byte) *realFace {
	font, err := sfnt.Parse(data)
	if err != nil || !fpdfAccepts(data) {
		return nil
	}
	name := fmt.Sprintf("real-%d", len(r.faces))
	r.pdf.AddUTF8FontFromBytes(name, "", data)
	return &realFace{name: name, font: font}
}

// fpdfAccepts tries a font on a throwaway document. fpdf takes only TrueType
// outlines, trusts the file it parses, and on a font it cannot read leaves an
// error that fails the whole PDF; a font from an untrusted .docx must not get
// that far, nor crash the conversion with a panic.
func fpdfAccepts(data []byte) (ok bool) {
	if len(data) < 4 {
		return false
	}
	if tag := binary.BigEndian.Uint32(data); tag != 0x00010000 && tag != 0x74727565 { // TrueType: 1.0 or "true"
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	probe := fpdf.New("P", "pt", "A4", "")
	probe.AddUTF8FontFromBytes("probe", "", data)
	probe.AddPage()
	probe.SetFont("probe", "", 10)
	probe.Text(10, 10, "Aa0")
	return probe.Output(io.Discard) == nil
}

// face is the real font for a family's face: embedded, or installed when
// system fonts are in use; nil when there is none.
func (r *fpdfRenderer) face(family string, bold, italic bool) *realFace {
	k := newFaceKey(family, bold, italic)
	f, seen := r.faces[k]
	if !seen {
		if data := r.system.lookup(k); data != nil {
			f = r.register(data)
		}
		r.faces[k] = f
	}
	return f
}

// covers reports whether f has a glyph for every character of s.
func (r *fpdfRenderer) covers(f *realFace, s string) bool {
	for _, c := range s {
		if i, err := f.font.GlyphIndex(&r.glyphs, c); err != nil || i == 0 {
			return false
		}
	}
	return true
}

// selectFont makes the backend's font the one to draw s in: the requested
// family's real font when there is one that has all of s's characters, else
// its metric-compatible replacement. It returns the text to draw, which in a
// replacement for a symbol font is s's Unicode stand-ins.
func (r *fpdfRenderer) selectFont(s string) string {
	c := r.cur
	if f := r.face(c.family, c.bold, c.italic); f != nil && r.covers(f, s) {
		r.pdf.SetFont(f.name, c.decoration(), c.size)
		return s
	}
	var style strings.Builder
	if c.bold {
		style.WriteByte('B')
	}
	if c.italic {
		style.WriteByte('I')
	}
	style.WriteString(c.decoration())
	r.pdf.SetFont(mapFontFamily(c.family), style.String(), c.size)
	return symbolText(c.family, s)
}

// decoration is fpdf's style letters for the request's underline ("U") and
// strikethrough ("S").
func (c fontRequest) decoration() string {
	var s string
	if c.underline {
		s += "U"
	}
	if c.strike {
		s += "S"
	}
	return s
}

// defaultRenderSizePt is only the backend's initial font size; every run resets
// the font before it is measured or drawn.
const defaultRenderSizePt = 11.0

func (r *fpdfRenderer) SetFont(family string, bold, italic, underline, strike bool, sizePt float64) {
	r.cur = fontRequest{family: family, bold: bold, italic: italic, underline: underline, strike: strike, size: sizePt}
}

// SetTextColor sets the text fill color from a "#RRGGBB" string; an empty or
// malformed value falls back to black so undeclared/"auto" runs draw normally.
func (r *fpdfRenderer) SetTextColor(colorHex string) {
	cr, cg, cb, ok := parseHexColor(colorHex)
	if !ok {
		cr, cg, cb = 0, 0, 0
	}
	r.pdf.SetTextColor(cr, cg, cb)
}

func (r *fpdfRenderer) TextWidth(s string) float64 {
	return r.pdf.GetStringWidth(r.selectFont(s))
}

// AddPage starts a page of the requested size (each section brings its own, so
// the size is passed per page rather than fixed at construction).
func (r *fpdfRenderer) AddPage(widthPt, heightPt float64) {
	r.pdf.AddPageFormat("P", fpdf.SizeType{Wd: widthPt, Ht: heightPt})
}

func (r *fpdfRenderer) DrawText(x, y float64, s string) {
	if s != "" {
		r.pdf.Text(x, y, r.selectFont(s))
	}
}

// DrawImage embeds img (once, keyed by its media part name) and draws it in the
// given rectangle. A picture the backend cannot parse (say a PNG corrupt past
// its header) is skipped: fpdf's error is sticky, so registering it on the real
// document would fail the whole PDF over one picture. It is therefore tried on
// a throwaway document first.
func (r *fpdfRenderer) DrawImage(x, y, w, h float64, img *Image) {
	if img == nil || w <= 0 || h <= 0 {
		return
	}
	opts := fpdf.ImageOptions{ImageType: img.Type}
	ok, seen := r.images[img.Name]
	if !seen {
		probe := fpdf.New("P", "pt", "A4", "")
		probe.RegisterImageOptionsReader(img.Name, opts, bytes.NewReader(img.Data))
		if ok = probe.Error() == nil; ok {
			r.pdf.RegisterImageOptionsReader(img.Name, opts, bytes.NewReader(img.Data))
		}
		r.images[img.Name] = ok
	}
	if ok {
		r.pdf.ImageOptions(img.Name, x, y, w, h, false, opts, 0, "")
	}
}

func (r *fpdfRenderer) FillRect(x, y, w, h float64, colorHex string) {
	cr, cg, cb, ok := parseHexColor(colorHex)
	if !ok {
		return
	}
	r.pdf.SetFillColor(cr, cg, cb)
	r.pdf.Rect(x, y, w, h, "F")
}

func (r *fpdfRenderer) StrokeLine(x1, y1, x2, y2, widthPt float64, colorHex string) {
	cr, cg, cb, ok := parseHexColor(colorHex)
	if !ok {
		return
	}
	r.pdf.SetDrawColor(cr, cg, cb)
	r.pdf.SetLineWidth(widthPt)
	r.pdf.Line(x1, y1, x2, y2)
}

// Rotate/RotateEnd bracket a rotated drawing block (a cell's vertical text).
// fpdf's own rotation is counter-clockwise about the given point, which is the
// convention the renderer interface documents.
func (r *fpdfRenderer) Rotate(deg, x, y float64) {
	r.pdf.TransformBegin()
	r.pdf.TransformRotate(deg, x, y)
}

func (r *fpdfRenderer) RotateEnd() { r.pdf.TransformEnd() }

// Clip/ClipEnd bracket a clipped drawing block (the cell of a fixed-height row).
func (r *fpdfRenderer) Clip(x, y, w, h float64) {
	r.pdf.ClipRect(x, y, w, h, false)
}

func (r *fpdfRenderer) ClipEnd() { r.pdf.ClipEnd() }

func (r *fpdfRenderer) Link(x, y, w, h float64, target string) {
	name, internal := strings.CutPrefix(target, "#")
	if !internal {
		r.pdf.LinkString(x, y, w, h, target)
		return
	}
	r.pdf.Link(x, y, w, h, r.anchor(name))
}

func (r *fpdfRenderer) Anchor(name string, y float64) {
	r.pdf.SetLink(r.anchor(name), y, -1)
	r.placed[name] = true
}

// anchor is the internal link id for a bookmark name, allocated on first use:
// a table of contents links to headings it has not reached yet.
func (r *fpdfRenderer) anchor(name string) int {
	id, ok := r.anchors[name]
	if !ok {
		id = r.pdf.AddLink()
		r.anchors[name] = id
	}
	return id
}

func (r *fpdfRenderer) Output(w io.Writer) (err error) {
	if err := r.pdf.Error(); err != nil {
		return err
	}
	// Writing subsets every real font to the characters drawn in it; a font
	// from an untrusted .docx that passed the probe could still trip fpdf here.
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("docx2pdf: write pdf: %v", p)
		}
	}()
	// A link to a bookmark the document never placed would point at page 0,
	// which does not exist; send it to the top of the first page instead.
	for name, id := range r.anchors {
		if !r.placed[name] {
			r.pdf.SetLink(id, 0, 1)
		}
	}
	return r.pdf.Output(w)
}

// parseHexColor parses a "#RRGGBB" string into 0-255 components. It returns
// ok=false for "" (no color declared) or any malformed value, so callers can
// treat it as a no-op rather than drawing a wrong color.
func parseHexColor(hex string) (r, g, b int, ok bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xFF), int(v >> 8 & 0xFF), int(v & 0xFF), true
}

// mapFontFamily maps a Word font name to one of the embedded families, chosen
// to keep line breaks close to Word's while still rendering Unicode (incl.
// Cyrillic) instead of mojibake: Calibri → Carlito (metric-compatible and
// Cyrillic-covering), other serif (incl. Cambria) → Liberation Serif, monospace
// → Liberation Mono, else Liberation Sans. Cambria's own metric clone Caladea is
// deliberately not used: it lacks Cyrillic glyphs, so it would render Cyrillic
// text as blank boxes. The Calibri case comes first because Carlito is sans by
// shape and would otherwise fall into the default Liberation Sans branch.
// Monospace is checked before serif, and an explicit "sans" before the serif
// list, so "DejaVu Sans Mono" stays monospace and "Microsoft Sans Serif" stays
// sans even though its name contains "serif".
func mapFontFamily(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "calibri"):
		return fonts.Carlito
	case containsAny(n, "courier", "mono", "consol", "menlo", "code", "monaco", "lucida console",
		"cascadia", "andale", "jetbrains", "inconsolata", "sfmono"):
		return fonts.Mono
	case strings.Contains(n, "sans"):
		return fonts.Sans
	case containsAny(n, "times", "serif", "georgia", "garamond", "roman", "cambria", "minion", "book",
		"constantia", "palatino", "baskerville", "didot", "playfair", "merriweather", "cardo",
		"goudy", "caslon", "bodoni", "rockwell", "perpetua"):
		return fonts.Serif
	default:
		return fonts.Sans
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
