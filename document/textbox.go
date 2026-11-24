package document

import (
	"cmp"
	"encoding/xml"
	"math"
	"strconv"
	"strings"
)

// Text boxes: a VML shape with a v:textbox, or a DrawingML wps:wsp with a
// wps:txbx. Each becomes a TextBox - a frame with a body of its own, drawn like
// a one-cell table - either in its line or, when positioned, floating.

// --- VML (w:pict) ---

// xmlVTextBox is <v:textbox>: its style (mso-fit-shape-to-text), its inset
// and its content.
type xmlVTextBox struct {
	Style, Inset string
	Content      xmlTextBoxes
}

func (tb *xmlVTextBox) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "style":
			tb.Style = a.Value
		case "inset":
			tb.Inset = a.Value
		}
	}
	return tb.Content.UnmarshalXML(d, start)
}

// xmlVFill is <v:fill>, and xmlVStroke <v:stroke>: a shape's fill and outline
// declared as child elements rather than attributes.
type xmlVFill struct {
	On    string `xml:"on,attr"`
	Color string `xml:"color,attr"`
}

type xmlVStroke struct {
	On     string `xml:"on,attr"`
	Color  string `xml:"color,attr"`
	Weight string `xml:"weight,attr"`
}

// xmlVWrap is <w10:wrap>: how the text wraps around a positioned shape.
type xmlVWrap struct {
	Type string `xml:"type,attr"`
}

// vmlOn reads a VML boolean ("t", "f", "true", …), def when absent.
func vmlOn(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return def
	case "f", "false", "0", "off":
		return false
	}
	return true
}

// vmlLength is a signed CSS length in points (a position may be negative).
func vmlLength(v string) float64 {
	if rest, ok := strings.CutPrefix(strings.TrimSpace(v), "-"); ok {
		return -cssLengthPt(rest)
	}
	return cssLengthPt(v)
}

// namedColors are the color names VML and DrawingML (a:prstClr) use.
var namedColors = map[string]string{
	"black": "#000000", "white": "#FFFFFF", "red": "#FF0000", "green": "#008000",
	"blue": "#0000FF", "yellow": "#FFFF00", "gray": "#808080", "grey": "#808080",
	"silver": "#C0C0C0", "maroon": "#800000", "navy": "#000080", "olive": "#808000",
	"purple": "#800080", "teal": "#008080", "aqua": "#00FFFF", "cyan": "#00FFFF",
	"fuchsia": "#FF00FF", "magenta": "#FF00FF", "lime": "#00FF00", "orange": "#FFA500",
}

// vmlColor resolves a VML color ("#BF9000", "#fc0", "red", "#bf9000 [2407]")
// into "#RRGGBB", or "" when it is none or unknown.
func vmlColor(v string) string {
	v, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(v)), " ")
	if hex, ok := strings.CutPrefix(v, "#"); ok {
		if len(hex) == 3 {
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		}
		h, _ := sRGBHex(hex)
		return h
	}
	return namedColors[v]
}

// box resolves a VML shape into a TextBox: sized and positioned by its style,
// filled and outlined as its attributes say. It is nil for a shape with no
// size, and for one that is neither a text box nor a rectangle (rect says it
// is one).
func (s *xmlVShape) box(sc styleContext, rect bool) *TextBox {
	if !rect && s.TextBox == nil {
		return nil
	}
	style := parseVMLStyle(s.Style)
	tb := &TextBox{WidthPt: cssLengthPt(style["width"]), HeightPt: cssLengthPt(style["height"])}
	if tb.WidthPt <= 0 || tb.HeightPt <= 0 {
		return nil
	}
	if vmlOn(s.Filled, true) && (s.Fill == nil || vmlOn(s.Fill.On, true)) {
		tb.Fill = "#FFFFFF"
		switch {
		case s.FillColor != "":
			tb.Fill = vmlColor(s.FillColor)
		case s.Fill != nil && s.Fill.Color != "":
			tb.Fill = vmlColor(s.Fill.Color)
		}
	}
	if vmlOn(s.Stroked, true) && (s.Stroke == nil || vmlOn(s.Stroke.On, true)) {
		color, weight := s.StrokeColor, s.StrokeWeight
		if s.Stroke != nil {
			color, weight = cmp.Or(color, s.Stroke.Color), cmp.Or(weight, s.Stroke.Weight)
		}
		tb.Border = BorderSide{Style: "single", WidthPt: 0.75, Color: cmp.Or(vmlColor(color), "#000000")}
		if w := cssLengthPt(weight); w > 0 {
			tb.Border.WidthPt = w
		}
	}
	tb.Insets = CellMargins{LeftPt: 7.2, TopPt: 3.6, RightPt: 7.2, BottomPt: 3.6}
	switch style["v-text-anchor"] {
	case "middle", "middle-center":
		tb.VAlign = VAlignCenter
	case "bottom", "bottom-center":
		tb.VAlign = VAlignBottom
	}
	if s.TextBox != nil {
		if s.TextBox.Inset != "" {
			sides := []*float64{&tb.Insets.LeftPt, &tb.Insets.TopPt, &tb.Insets.RightPt, &tb.Insets.BottomPt}
			for i, part := range strings.Split(s.TextBox.Inset, ",") {
				if part = strings.TrimSpace(part); i < len(sides) && part != "" {
					*sides[i] = max(cssLengthPt(part), 0)
				}
			}
		}
		tb.AutoFit = parseVMLStyle(s.TextBox.Style)["mso-fit-shape-to-text"] == "t" || style["mso-fit-shape-to-text"] == "t"
		tb.Content = buildBoxContent(s.TextBox.Content.Blocks, sc)
	}
	if style["position"] == "absolute" {
		tb.Float = vmlFloat(style, s.Wrap)
	}
	return tb
}

// vmlFloat resolves a positioned VML shape's placement: its offsets
// (margin-left/top) from what mso-position-*-relative names - by default its
// column and paragraph - or its alignment there; and its wrap, by default none
// (the shape in front of the text, unless its z-index puts it behind).
func vmlFloat(style map[string]string, wrap *xmlVWrap) *Float {
	fl := &Float{
		HorzAnchor: anchorFrom(strings.TrimSuffix(style["mso-position-horizontal-relative"], "-margin-area")),
		VertAnchor: anchorFrom(strings.TrimSuffix(style["mso-position-vertical-relative"], "-margin-area")),
		XPt:        vmlLength(cmp.Or(style["margin-left"], style["left"])),
		YPt:        vmlLength(cmp.Or(style["margin-top"], style["top"])),
		Wrap:       WrapNone,
	}
	if h := style["mso-position-horizontal"]; h != "absolute" {
		fl.XSpec = h
	}
	if v := style["mso-position-vertical"]; v != "absolute" {
		fl.YSpec = v
	}
	if wrap != nil {
		switch strings.ToLower(wrap.Type) {
		case "square", "tight", "through":
			fl.Wrap = WrapAround
		case "topandbottom":
			fl.Wrap = WrapTopAndBottom
		}
	}
	z, _ := strconv.Atoi(style["z-index"])
	fl.InFront = fl.Wrap == WrapNone && z >= 0
	return fl
}

// boxes resolves the picture's text boxes and rectangles.
func (p *xmlPict) boxes(sc styleContext) []*TextBox {
	var out []*TextBox
	if p.Shape != nil && p.Shape.ImageData == nil {
		if tb := p.Shape.box(sc, false); tb != nil {
			out = append(out, tb)
		}
	}
	for _, list := range [][]xmlVShape{p.Rects, p.RoundRects} {
		for i := range list {
			if tb := list[i].box(sc, true); tb != nil {
				out = append(out, tb)
			}
		}
	}
	return out
}

// --- DrawingML (wps:wsp) ---

// xmlWsp is a DrawingML shape: its geometry, fill and outline (wps:spPr), its
// text insets and anchoring (wps:bodyPr), and its text box (wps:txbx).
type xmlWsp struct {
	SpPr struct {
		Geom *struct {
			Prst string `xml:"prst,attr"`
		} `xml:"http://schemas.openxmlformats.org/drawingml/2006/main prstGeom"`
		NoFill    *struct{}     `xml:"http://schemas.openxmlformats.org/drawingml/2006/main noFill"`
		SolidFill *xmlSolidFill `xml:"http://schemas.openxmlformats.org/drawingml/2006/main solidFill"`
		Ln        *struct {
			W         string        `xml:"w,attr"`
			NoFill    *struct{}     `xml:"http://schemas.openxmlformats.org/drawingml/2006/main noFill"`
			SolidFill *xmlSolidFill `xml:"http://schemas.openxmlformats.org/drawingml/2006/main solidFill"`
		} `xml:"http://schemas.openxmlformats.org/drawingml/2006/main ln"`
	} `xml:"http://schemas.microsoft.com/office/word/2010/wordprocessingShape spPr"`
	BodyPr struct {
		LIns      *string   `xml:"lIns,attr"`
		TIns      *string   `xml:"tIns,attr"`
		RIns      *string   `xml:"rIns,attr"`
		BIns      *string   `xml:"bIns,attr"`
		Anchor    string    `xml:"anchor,attr"`
		SpAutoFit *struct{} `xml:"http://schemas.openxmlformats.org/drawingml/2006/main spAutoFit"`
	} `xml:"http://schemas.microsoft.com/office/word/2010/wordprocessingShape bodyPr"`
	Txbx *xmlTextBoxes `xml:"http://schemas.microsoft.com/office/word/2010/wordprocessingShape txbx"`
}

// xmlSolidFill is <a:solidFill>: an RGB, theme, system or named color.
type xmlSolidFill struct {
	Srgb *struct {
		Val string `xml:"val,attr"`
	} `xml:"http://schemas.openxmlformats.org/drawingml/2006/main srgbClr"`
	Scheme *struct {
		Val string `xml:"val,attr"`
	} `xml:"http://schemas.openxmlformats.org/drawingml/2006/main schemeClr"`
	Sys *struct {
		LastClr string `xml:"lastClr,attr"`
	} `xml:"http://schemas.openxmlformats.org/drawingml/2006/main sysClr"`
	Prst *struct {
		Val string `xml:"val,attr"`
	} `xml:"http://schemas.openxmlformats.org/drawingml/2006/main prstClr"`
}

// schemeSlots maps a:schemeClr's values onto the theme's color slots.
var schemeSlots = map[string]string{"tx1": "dk1", "bg1": "lt1", "tx2": "dk2", "bg2": "lt2"}

// color resolves the fill into "#RRGGBB", "" when it cannot be.
func (f *xmlSolidFill) color(theme map[string]string) string {
	switch {
	case f == nil:
		return ""
	case f.Srgb != nil:
		h, _ := sRGBHex(f.Srgb.Val)
		return h
	case f.Scheme != nil:
		return theme[cmp.Or(schemeSlots[f.Scheme.Val], f.Scheme.Val)]
	case f.Sys != nil:
		h, _ := sRGBHex(f.Sys.LastClr)
		return h
	case f.Prst != nil:
		return namedColors[strings.ToLower(f.Prst.Val)]
	}
	return ""
}

// box resolves a DrawingML shape of the given extent into a TextBox, or nil
// for a shape that is neither a text box nor a rectangle.
func (s *xmlWsp) box(ext xmlExtent, sc styleContext) *TextBox {
	prst := ""
	if s.SpPr.Geom != nil {
		prst = s.SpPr.Geom.Prst
	}
	if s.Txbx == nil && prst != "rect" && prst != "roundRect" {
		return nil
	}
	tb := &TextBox{WidthPt: emuToPt(ext.Cx), HeightPt: emuToPt(ext.Cy)}
	if tb.WidthPt <= 0 || tb.HeightPt <= 0 {
		return nil
	}
	if s.SpPr.NoFill == nil {
		tb.Fill = s.SpPr.SolidFill.color(sc.theme)
	}
	if ln := s.SpPr.Ln; ln != nil && ln.NoFill == nil {
		tb.Border = BorderSide{Style: "single", WidthPt: 0.75, Color: cmp.Or(ln.SolidFill.color(sc.theme), "#000000")}
		if w := emuToPt(ln.W); w > 0 {
			tb.Border.WidthPt = w
		}
	}
	inset := func(v *string, def float64) float64 {
		if v == nil {
			return def
		}
		return max(emuToPt(*v), 0)
	}
	bp := s.BodyPr
	tb.Insets = CellMargins{LeftPt: inset(bp.LIns, 7.2), TopPt: inset(bp.TIns, 3.6), RightPt: inset(bp.RIns, 7.2), BottomPt: inset(bp.BIns, 3.6)}
	switch bp.Anchor {
	case "ctr":
		tb.VAlign = VAlignCenter
	case "b":
		tb.VAlign = VAlignBottom
	}
	tb.AutoFit = bp.SpAutoFit != nil
	if s.Txbx != nil {
		tb.Content = buildBoxContent(s.Txbx.Blocks, sc)
	}
	return tb
}

// boxes resolves the drawing's text boxes and rectangles; a floating one
// (wp:anchor) comes with its placement.
func (d *xmlDrawing) boxes(sc styleContext) []*TextBox {
	var out []*TextBox
	for _, a := range d.Inline {
		if a.Graphic.Data.Wsp != nil {
			if tb := a.Graphic.Data.Wsp.box(a.Extent, sc); tb != nil {
				out = append(out, tb)
			}
		}
	}
	for _, a := range d.Anchor {
		if a.Graphic.Data.Wsp != nil {
			if tb := a.Graphic.Data.Wsp.box(a.Extent, sc); tb != nil {
				tb.Float = a.float()
				out = append(out, tb)
			}
		}
	}
	return out
}

// buildBoxContent builds a text box's body. It is a body of its own: the
// formatting of a table the box's anchor stands in does not reach into it.
func buildBoxContent(blocks []xmlBodyChild, sc styleContext) []BodyElement {
	sc.tablePPr, sc.tableRPr = nil, nil
	body, _ := buildBody(xmlBody{Children: blocks}, sc)
	return body
}

// --- Layout ---

// textBoxLayout is a text box laid out for drawing: the cell its content is
// set in, and its drawn size.
type textBoxLayout struct {
	cell cellLayout
	w, h float64
	// clip cuts off content taller than a box of fixed height, as Word does.
	clip bool
}

func layoutTextBox(r renderer, tb TextBox) textBoxLayout {
	cell := Cell{
		Content: tb.Content,
		Shading: tb.Fill,
		Borders: CellBorders{Top: tb.Border, Bottom: tb.Border, Left: tb.Border, Right: tb.Border},
		Margins: tb.Insets,
		VAlign:  tb.VAlign,
	}
	content := layoutCellContent(r, cell, tb.Insets, tb.WidthPt, tb.WidthPt, math.Inf(1))
	need := content.height + tb.Insets.TopPt + tb.Insets.BottomPt
	h := tb.HeightPt
	if tb.AutoFit {
		h = need
	}
	return textBoxLayout{
		cell: cellLayout{cell: cell, margins: tb.Insets, content: content},
		w:    tb.WidthPt, h: h,
		clip: need > h+fitTolerancePt,
	}
}

// draw draws the box with its top-left corner at (x, y).
func (tl textBoxLayout) draw(r renderer, x, y float64) {
	drawCellBox(r, tl.cell, x, y, tl.w, tl.h, tl.clip, wholeContent)
}
