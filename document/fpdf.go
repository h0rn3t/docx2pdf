package document

import (
	"bytes"
	"io"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"

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
// sized from the document's page geometry rather than a fixed A4.
// simplified: no SetMargins - auto page breaks are off and the layout positions
// all text by absolute coordinates, so fpdf's own margins have no effect here.
func newFPDFRenderer(widthPt, heightPt float64) *fpdfRenderer {
	pdf := fpdf.NewCustom(&fpdf.InitType{
		UnitStr: "pt",
		Size:    fpdf.SizeType{Wd: widthPt, Ht: heightPt},
	})
	pdf.SetAutoPageBreak(false, 0) // pagination is handled by the layout
	pdf.AliasNbPages(nbPagesAlias) // NUMPAGES fields draw the alias; fpdf fills the count
	for _, family := range fonts.Families {
		for _, s := range utf8Styles {
			pdf.AddUTF8FontFromBytes(family, s.str, fonts.Bytes(family, s.bold, s.italic))
		}
	}
	pdf.SetFont(fonts.Sans, "", defaultRenderSizePt) // a font must be set before any text op
	return &fpdfRenderer{pdf: pdf, images: make(map[string]bool)}
}

// defaultRenderSizePt is only the backend's initial font size; every run resets
// the font before it is measured or drawn.
const defaultRenderSizePt = 11.0

func (r *fpdfRenderer) SetFont(family string, bold, italic, underline bool, sizePt float64) {
	var style strings.Builder
	if bold {
		style.WriteByte('B')
	}
	if italic {
		style.WriteByte('I')
	}
	if underline {
		style.WriteByte('U')
	}
	r.pdf.SetFont(mapFontFamily(family), style.String(), sizePt)
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

func (r *fpdfRenderer) TextWidth(s string) float64 { return r.pdf.GetStringWidth(s) }

// AddPage starts a page of the requested size (each section brings its own, so
// the size is passed per page rather than fixed at construction).
func (r *fpdfRenderer) AddPage(widthPt, heightPt float64) {
	r.pdf.AddPageFormat("P", fpdf.SizeType{Wd: widthPt, Ht: heightPt})
}

func (r *fpdfRenderer) DrawText(x, y float64, s string) {
	if s != "" {
		r.pdf.Text(x, y, s)
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

func (r *fpdfRenderer) Output(w io.Writer) error {
	if err := r.pdf.Error(); err != nil {
		return err
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
