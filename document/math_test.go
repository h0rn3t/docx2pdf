package document

import (
	"testing"

	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

const mathXmlns = ` xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"`

// renderBody converts a one-section fixture with the given body and returns
// what it drew.
func renderBody(t *testing.T, body string) *fakeRenderer {
	t.Helper()
	doc := openFixture(t, docxtest.Build(t, body+hrSectPr))
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)
	return f
}

func TestMath_QuadraticFormula(t *testing.T) {
	f := renderBody(t, `<w:p><m:oMathPara`+mathXmlns+`><m:oMath>`+
		`<m:r><m:t>x=</m:t></m:r>`+
		`<m:f><m:num><m:r><m:t>-b±</m:t></m:r>`+
		`<m:rad><m:radPr><m:degHide m:val="1"/></m:radPr><m:deg/><m:e>`+
		`<m:sSup><m:e><m:r><m:t>b</m:t></m:r></m:e><m:sup><m:r><m:t>2</m:t></m:r></m:sup></m:sSup>`+
		`<m:r><m:t>-4ac</m:t></m:r></m:e></m:rad></m:num>`+
		`<m:den><m:r><m:t>2a</m:t></m:r></m:den></m:f></m:oMath></m:oMathPara></w:p>`)

	x, b, sup, den := drawOf(t, f, "x"), drawOf(t, f, "b"), drawOf(t, f, "2"), drawOf(t, f, "a") // "2a" is drawn as an upright 2 and an italic a
	if !(b.y < x.y && x.y < den.y) {
		t.Errorf("numerator y=%v, x y=%v, denominator y=%v: want the numerator above the baseline and the denominator below", b.y, x.y, den.y)
	}
	if !(sup.y < b.y && sup.size < b.size) {
		t.Errorf("superscript at y=%v size %v, base at y=%v size %v: want it raised and smaller", sup.y, sup.size, b.y, b.size)
	}
	drawOf(t, f, "−") // the hyphen-minus is drawn as a minus sign
	drawOf(t, f, "±")

	var bar, radical bool
	for _, s := range f.strokes {
		if s.y1 == s.y2 && s.y1 > b.y && s.y1 < den.y && s.x2-s.x1 > 40 {
			bar = true
		}
		if s.y1 != s.y2 {
			radical = true
		}
	}
	if !bar {
		t.Errorf("no fraction bar between y=%v and y=%v in %+v", b.y, den.y, f.strokes)
	}
	if !radical {
		t.Errorf("no slanted radical stroke in %+v", f.strokes)
	}
	// m:oMathPara centers its equation: the column is 85.05..552.8 pt.
	if left := 1701.0 / 20; x.x < left+100 {
		t.Errorf("equation starts at x=%v, want it centered in the column", x.x)
	}
}

func TestMath_InlineFlowsWithText(t *testing.T) {
	f := renderBody(t, `<w:p><w:r><w:t xml:space="preserve">Нехай </w:t></w:r>`+
		`<m:oMath`+mathXmlns+`><m:r><m:t>y</m:t></m:r></m:oMath>`+
		`<w:r><w:t xml:space="preserve"> більше</w:t></w:r></w:p>`)
	before, y, after := drawOf(t, f, "Нехай"), drawOf(t, f, "y"), drawOf(t, f, "більше")
	if y.y != before.y || after.y != before.y {
		t.Errorf("baselines %v, %v, %v: want the equation on the text's line", before.y, y.y, after.y)
	}
	if !(before.x < y.x && y.x < after.x) {
		t.Errorf("x positions %v, %v, %v: want the equation between the words", before.x, y.x, after.x)
	}
}

func TestMath_Structures(t *testing.T) {
	tests := []struct {
		name, omml string
		want       []string
	}{
		{"delimiters", `<m:d><m:e><m:r><m:t>a</m:t></m:r></m:e></m:d>`, []string{"(", "a", ")"}},
		{"sum with limits", `<m:nary><m:naryPr><m:chr m:val="∑"/></m:naryPr><m:sub><m:r><m:t>i</m:t></m:r></m:sub>` +
			`<m:sup><m:r><m:t>n</m:t></m:r></m:sup><m:e><m:r><m:t>k</m:t></m:r></m:e></m:nary>`, []string{"∑", "i", "n", "k"}},
		{"function", `<m:func><m:fName><m:r><m:rPr><m:sty m:val="p"/></m:rPr><m:t>sin</m:t></m:r></m:fName>` +
			`<m:e><m:r><m:t>θ</m:t></m:r></m:e></m:func>`, []string{"sin", "θ"}},
		{"matrix", `<m:m><m:mr><m:e><m:r><m:t>1</m:t></m:r></m:e><m:e><m:r><m:t>0</m:t></m:r></m:e></m:mr>` +
			`<m:mr><m:e><m:r><m:t>3</m:t></m:r></m:e><m:e><m:r><m:t>4</m:t></m:r></m:e></m:mr></m:m>`, []string{"1", "0", "3", "4"}},
		{"unknown element keeps its text", `<m:future><m:e><m:r><m:t>q</m:t></m:r></m:e></m:future>`, []string{"q"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := renderBody(t, `<w:p><m:oMath`+mathXmlns+`>`+tt.omml+`</m:oMath></w:p>`)
			for _, s := range tt.want {
				drawOf(t, f, s)
			}
		})
	}
}
