package document

import (
	"math"
	"slices"
	"testing"
)

const (
	vmlXmlns = ` xmlns:v="urn:schemas-microsoft-com:vml"`
	wpsXmlns = ` xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"` +
		` xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"` +
		` xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape"`
	// The column of hrSectPr starts here.
	hrLeft, hrTop = 1701.0 / 20, 1134.0 / 20
)

func fillOf(t *testing.T, f *fakeRenderer, color string) fillCall {
	t.Helper()
	i := slices.IndexFunc(f.fills, func(c fillCall) bool { return c.color == color })
	if i < 0 {
		t.Fatalf("no fill in %s among %+v", color, f.fills)
	}
	return f.fills[i]
}

func drawIndex(f *fakeRenderer, text string) int {
	return slices.IndexFunc(f.draws, func(d drawCall) bool { return d.text == text })
}

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// The VML text box of 04-complex.docx: positioned absolutely from its column
// and paragraph, filled and outlined, in front of the text after it.
func TestTextBox_VMLFloating(t *testing.T) {
	f := renderBody(t, `<w:p><w:r><w:t>anchor</w:t><w:pict><v:rect`+vmlXmlns+
		` style="position:absolute;margin-left:300pt;margin-top:10pt;width:160pt;height:60pt;z-index:1"`+
		` fillcolor="#FFF2CC" strokecolor="#BF9000"><v:textbox><w:txbxContent>`+
		`<w:p><w:r><w:t>inside</w:t></w:r></w:p></w:txbxContent></v:textbox></v:rect></w:pict></w:r></w:p>`+
		`<w:p><w:r><w:t>after</w:t></w:r></w:p>`)

	fill := fillOf(t, f, "#FFF2CC")
	if !approx(fill.x, hrLeft+300) || !approx(fill.y, hrTop+10) || fill.w != 160 || fill.h != 60 {
		t.Errorf("box fill = %+v, want 160×60 at (%v, %v)", fill, hrLeft+300, hrTop+10)
	}
	var outline int
	for _, s := range f.strokes {
		if s.color == "#BF9000" {
			outline++
		}
	}
	if outline != 4 {
		t.Errorf("%d strokes in #BF9000, want the box's 4 sides", outline)
	}
	inside := drawOf(t, f, "inside")
	if !approx(inside.x, fill.x+7.2) || inside.y < fill.y || inside.y > fill.y+fill.h {
		t.Errorf("box text at (%v, %v), want it inside the box, 7.2pt from its left edge", inside.x, inside.y)
	}
	if drawOf(t, f, "anchor").y != hrTop+11 {
		t.Errorf("anchor paragraph moved: its text is at y=%v", drawOf(t, f, "anchor").y)
	}
	if drawIndex(f, "inside") < drawIndex(f, "after") {
		t.Error("box drawn before the text after it; a VML box without a wrap is in front of the text")
	}
}

func TestTextBox_VMLInline(t *testing.T) {
	f := renderBody(t, `<w:p><w:r><w:t>before</w:t></w:r><w:r><w:pict><v:shape`+vmlXmlns+
		` style="width:72pt;height:36pt" fillcolor="#FF0000" stroked="f"><v:textbox inset="0,0,0,0"><w:txbxContent>`+
		`<w:p><w:r><w:t>inside</w:t></w:r></w:p></w:txbxContent></v:textbox></v:shape></w:pict></w:r></w:p>`)

	before, inside, fill := drawOf(t, f, "before"), drawOf(t, f, "inside"), fillOf(t, f, "#FF0000")
	if !approx(fill.x, before.x+6*5.5) || !approx(fill.y+fill.h, before.y) || fill.w != 72 || fill.h != 36 {
		t.Errorf("box fill = %+v, want 72×36 right after %q, on its baseline y=%v", fill, "before", before.y)
	}
	if !approx(inside.x, fill.x) {
		t.Errorf("box text at x=%v, want the box's left edge %v (no inset)", inside.x, fill.x)
	}
	if len(f.strokes) != 0 {
		t.Errorf("strokes = %+v, want none for stroked=\"f\"", f.strokes)
	}
}

func TestTextBox_DrawingMLAnchored(t *testing.T) {
	f := renderBody(t, `<w:p><w:r><w:drawing><wp:anchor`+wpsXmlns+`>`+
		`<wp:positionH relativeFrom="page"><wp:posOffset>914400</wp:posOffset></wp:positionH>`+
		`<wp:positionV relativeFrom="page"><wp:posOffset>914400</wp:posOffset></wp:positionV>`+
		`<wp:extent cx="1828800" cy="457200"/><wp:wrapNone/>`+
		`<a:graphic><a:graphicData uri="http://schemas.microsoft.com/office/word/2010/wordprocessingShape"><wps:wsp>`+
		`<wps:spPr><a:prstGeom prst="rect"/><a:solidFill><a:srgbClr val="00FF00"/></a:solidFill>`+
		`<a:ln w="12700"><a:solidFill><a:srgbClr val="0000FF"/></a:solidFill></a:ln></wps:spPr>`+
		`<wps:txbx><w:txbxContent><w:p><w:r><w:t>inside</w:t></w:r></w:p></w:txbxContent></wps:txbx>`+
		`<wps:bodyPr lIns="0" tIns="0" rIns="0" bIns="0" anchor="ctr"/></wps:wsp>`+
		`</a:graphicData></a:graphic></wp:anchor></w:drawing></w:r></w:p>`)

	fill := fillOf(t, f, "#00FF00")
	if fill.x != 72 || fill.y != 72 || fill.w != 144 || fill.h != 36 {
		t.Errorf("box fill = %+v, want 144×36 at (72, 72) on the page", fill)
	}
	for _, s := range f.strokes {
		if s.color != "#0000FF" || s.widthPt != 1 {
			t.Errorf("stroke %+v, want the 1pt #0000FF outline", s)
		}
	}
	inside := drawOf(t, f, "inside")
	// One 11pt line (13.2pt tall) centered in 36pt: its baseline is 11.4+11 down.
	if inside.x != 72 || !approx(inside.y, 72+11.4+11) {
		t.Errorf("box text at (%v, %v), want (72, %v): no insets, centered vertically", inside.x, inside.y, 72+11.4+11)
	}
}

func TestTextBox_FitsShapeToText(t *testing.T) {
	f := renderBody(t, `<w:p><w:r><w:pict><v:rect`+vmlXmlns+
		` style="position:absolute;width:100pt;height:10pt" fillcolor="#00FFFF">`+
		`<v:textbox style="mso-fit-shape-to-text:t"><w:txbxContent>`+
		`<w:p><w:r><w:t>one</w:t></w:r></w:p><w:p><w:r><w:t>two</w:t></w:r></w:p>`+
		`</w:txbxContent></v:textbox></v:rect></w:pict></w:r></w:p>`)

	// Two 13.2pt lines and the default 3.6pt insets above and below.
	if fill := fillOf(t, f, "#00FFFF"); !approx(fill.h, 2*13.2+2*3.6) {
		t.Errorf("box height = %v, want it grown to its text, %v", fill.h, 2*13.2+2*3.6)
	}
	if len(f.clips) != 0 {
		t.Errorf("clips = %+v, want none for a box that fits its text", f.clips)
	}
}
