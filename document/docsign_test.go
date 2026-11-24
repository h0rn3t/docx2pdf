package document

import (
	"bytes"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/h0rn3t/docx2pdf/document/fonts"
	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

// The page of the HR templates: A4 with 1701/850 dxa side margins, so the text
// column is exactly 9355 dxa (467.75 pt) wide.
const hrSectPr = `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/>` +
	`<w:pgMar w:top="1134" w:right="850" w:bottom="1134" w:left="1701"/></w:sectPr>`

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// drawOf returns the draw of text, failing the test when there is none.
func drawOf(t *testing.T, f *fakeRenderer, text string) drawCall {
	t.Helper()
	for _, d := range f.draws {
		if d.text == text {
			return d
		}
	}
	t.Fatalf("no draw of %q in %+v", text, f.draws)
	return drawCall{}
}

func TestRightTabOnTheRightMargin(t *testing.T) {
	tests := []struct {
		name, runs, after, before string
	}{
		{"date and signature", `<w:r><w:t>17.11.2026</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>_____</w:t></w:r>`, "_____", "17.11.2026"},
		{"number after the date", `<w:r><w:t>17.11.2026</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>№ ___</w:t></w:r>`, "___", "17.11.2026"},
		{"tab opens the line", `<w:r><w:tab/></w:r><w:r><w:t>підпис</w:t></w:r>`, "підпис", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := openFixture(t, docxtest.Build(t,
				`<w:p><w:pPr><w:tabs><w:tab w:val="right" w:pos="9355"/></w:tabs></w:pPr>`+
					strings.ReplaceAll(tt.runs, "<w:r>", `<w:r><w:rPr><w:sz w:val="24"/></w:rPr>`)+`</w:p>`+hrSectPr))
			f := &fakeRenderer{}
			ConvertToPdf(doc).render(f)

			after := drawOf(t, f, tt.after)
			if tt.before != "" {
				if before := drawOf(t, f, tt.before); after.y != before.y {
					t.Errorf("%q drawn at y=%v, %q at y=%v: want one line", tt.after, after.y, tt.before, before.y)
				}
			}
			right := after.x + f.TextWidth(tt.after)
			if want := 1701.0/20 + 9355.0/20; !near(right, want) {
				t.Errorf("%q ends at x=%v, want the stop %v", tt.after, right, want)
			}
		})
	}
}

// A custom stop past the right margin is honored as in Word: the text after it
// runs into the margin up to the stop instead of wrapping.
func TestRightTabBeyondTheRightMargin(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t,
		`<w:p><w:pPr><w:tabs><w:tab w:val="right" w:leader="dot" w:pos="9900"/></w:tabs></w:pPr>`+
			`<w:r><w:t>Директор</w:t><w:tab/><w:t>Ім'я ПРІЗВИЩЕ</w:t></w:r></w:p>`+hrSectPr))
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	before, after := drawOf(t, f, "Директор"), drawOf(t, f, "ПРІЗВИЩЕ")
	if after.y != before.y {
		t.Errorf("%q drawn at y=%v, %q at y=%v: want one line", "ПРІЗВИЩЕ", after.y, "Директор", before.y)
	}
	right := after.x + f.TextWidth("ПРІЗВИЩЕ")
	if want := 1701.0/20 + 9900.0/20; !near(right, want) {
		t.Errorf("%q ends at x=%v, want the stop %v", "ПРІЗВИЩЕ", right, want)
	}
}

func TestParagraphBorders_Parse(t *testing.T) {
	bottom := BorderSide{Style: "single", WidthPt: 0.75, Color: "#000000", SpacePt: 2}
	tests := []struct {
		name, body, styles string
		want               ParagraphBorders
	}{
		{
			name: "inline bottom",
			body: `<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="2" w:color="auto"/></w:pBdr></w:pPr></w:p>`,
			want: ParagraphBorders{Bottom: bottom},
		},
		{
			name: "all sides and between",
			body: `<w:p><w:pPr><w:pBdr>` +
				`<w:top w:val="double" w:sz="4" w:space="1" w:color="FF0000"/>` +
				`<w:left w:val="single" w:sz="8" w:space="4"/>` +
				`<w:right w:val="single" w:sz="8" w:space="4"/>` +
				`<w:between w:val="single" w:sz="6" w:space="2"/>` +
				`</w:pBdr></w:pPr></w:p>`,
			want: ParagraphBorders{
				Top:     BorderSide{Style: "double", WidthPt: 0.5, Color: "#FF0000", SpacePt: 1},
				Left:    BorderSide{Style: "single", WidthPt: 1, Color: "#000000", SpacePt: 4},
				Right:   BorderSide{Style: "single", WidthPt: 1, Color: "#000000", SpacePt: 4},
				Between: bottom,
			},
		},
		{
			name: "none removes the side",
			body: `<w:p><w:pPr><w:pBdr><w:bottom w:val="none" w:sz="6"/></w:pBdr></w:pPr></w:p>`,
		},
		{
			name:   "from the paragraph style",
			body:   `<w:p><w:pPr><w:pStyle w:val="Ruled"/></w:pPr></w:p>`,
			styles: `<w:style w:type="paragraph" w:styleId="Ruled"><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="2"/></w:pBdr></w:pPr></w:style>`,
			want:   ParagraphBorders{Bottom: bottom},
		},
		{
			name:   "inline over the style",
			body:   `<w:p><w:pPr><w:pStyle w:val="Ruled"/><w:pBdr><w:top w:val="single" w:sz="6" w:space="2"/></w:pBdr></w:pPr></w:p>`,
			styles: `<w:style w:type="paragraph" w:styleId="Ruled"><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="2"/></w:pBdr></w:pPr></w:style>`,
			want:   ParagraphBorders{Top: bottom},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := openFixture(t, docxtest.BuildWithStyles(t, tt.body, tt.styles))
			if got := doc.Body[0].Paragraph.Props.Borders; got != tt.want {
				t.Errorf("Borders = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParagraphBorders_Render(t *testing.T) {
	black := func(w, space float64) BorderSide {
		return BorderSide{Style: "single", WidthPt: w, Color: "#000000", SpacePt: space}
	}
	left, right := testPage.originX, testPage.originX+contentWidthPt
	top := testPage.originY
	const lineH = 12 * lineSpacing // one 12 pt line
	tests := []struct {
		name  string
		paras []Paragraph
		want  []strokeCall
		// textTop is where the second paragraph's line box starts, or 0.
		textTop float64
	}{
		{
			name: "bottom rule between the indents, space below the text",
			paras: []Paragraph{
				{Runs: []Run{run("ab", 12)}, Props: ParagraphProperties{Indent: Indent{LeftPt: 210, RightPt: 10}, Borders: ParagraphBorders{Bottom: black(0.75, 2)}}},
				{Runs: []Run{run("cd", 12)}},
			},
			want:    []strokeCall{{x1: left + 210, y1: top + lineH + 2.375, x2: right - 10, y2: top + lineH + 2.375, widthPt: 0.75}},
			textTop: top + lineH + 2.75,
		},
		{
			name: "empty paragraph still draws its rule",
			paras: []Paragraph{
				{Props: ParagraphProperties{Borders: ParagraphBorders{Bottom: black(1, 0)}}},
			},
			want: []strokeCall{{x1: left, y1: top + defaultRenderSizePt*lineSpacing + 0.5, x2: right, y2: top + defaultRenderSizePt*lineSpacing + 0.5, widthPt: 1}},
		},
		{
			name: "top rule pushes the text down",
			paras: []Paragraph{
				{Runs: []Run{run("ab", 12)}, Props: ParagraphProperties{Spacing: Spacing{BeforePt: 6}, Borders: ParagraphBorders{Top: black(1, 4)}}},
				{Runs: []Run{run("cd", 12)}},
			},
			want:    []strokeCall{{x1: left, y1: top + 6.5, x2: right, y2: top + 6.5, widthPt: 1}},
			textTop: top + 6 + 1 + 4 + lineH,
		},
		{
			name: "double rule is two strokes",
			paras: []Paragraph{
				{Runs: []Run{run("ab", 12)}, Props: ParagraphProperties{Borders: ParagraphBorders{Bottom: BorderSide{Style: "double", WidthPt: 0.5, Color: "#000000"}}}},
			},
			want: []strokeCall{
				{x1: left, y1: top + lineH + 0.25, x2: right, y2: top + lineH + 0.25, widthPt: 0.5},
				{x1: left, y1: top + lineH + 1.25, x2: right, y2: top + lineH + 1.25, widthPt: 0.5},
			},
		},
		{
			name: "left and right rules outside the text, top rule spans them",
			paras: []Paragraph{
				{Runs: []Run{run("ab", 12)}, Props: ParagraphProperties{Borders: ParagraphBorders{Top: black(1, 0), Left: black(1, 4), Right: black(1, 4)}}},
			},
			want: []strokeCall{
				{x1: left - 5, y1: top + 0.5, x2: right + 5, y2: top + 0.5, widthPt: 1},
				{x1: left - 4.5, y1: top, x2: left - 4.5, y2: top + 1, widthPt: 1},
				{x1: right + 4.5, y1: top, x2: right + 4.5, y2: top + 1, widthPt: 1},
				{x1: left - 4.5, y1: top + 1, x2: left - 4.5, y2: top + 1 + lineH, widthPt: 1},
				{x1: right + 4.5, y1: top + 1, x2: right + 4.5, y2: top + 1 + lineH, widthPt: 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRenderer{}
			ConvertToPdf(&Document{Body: bodyOf(tt.paras...)}).render(f)
			assertStrokes(t, f.strokes, tt.want)
			if tt.textTop != 0 {
				if got := drawOf(t, f, "cd").y - 12; !near(got, tt.textTop) {
					t.Errorf("next paragraph's text top = %v, want %v", got, tt.textTop)
				}
			}
		})
	}
}

func assertStrokes(t *testing.T, got, want []strokeCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strokes = %+v, want %+v", got, want)
	}
	for i, g := range got {
		w := want[i]
		if !near(g.x1, w.x1) || !near(g.y1, w.y1) || !near(g.x2, w.x2) || !near(g.y2, w.y2) || !near(g.widthPt, w.widthPt) {
			t.Errorf("stroke %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestParagraphBorders_GroupDrawsOneBox(t *testing.T) {
	bdr := `<w:pBdr><w:top w:val="single" w:sz="8"/><w:bottom w:val="single" w:sz="8"/><w:between w:val="single" w:sz="8"/></w:pBdr>`
	p := func(ind string) string {
		return `<w:p><w:pPr>` + bdr + ind + `</w:pPr><w:r><w:rPr><w:sz w:val="24"/></w:rPr><w:t>a</w:t></w:r></w:p>`
	}
	const lineH = 12 * lineSpacing
	top := testPage.originY
	tests := []struct {
		name  string
		body  string
		wantY []float64 // the horizontal rules, top to bottom
	}{
		{"same borders join", p("") + p(""), []float64{top + 0.5, top + 1 + lineH + 0.5, top + 2 + 2*lineH + 0.5}},
		{"another indent starts a new box", p("") + p(`<w:ind w:left="720"/>`), []float64{top + 0.5, top + 1 + lineH + 0.5, top + 2 + lineH + 0.5, top + 3 + 2*lineH + 0.5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := openFixture(t, docxtest.Build(t, tt.body))
			f := &fakeRenderer{}
			ConvertToPdf(doc).render(f)
			var got []float64
			for _, s := range f.strokes {
				got = append(got, s.y1)
			}
			if len(got) != len(tt.wantY) {
				t.Fatalf("rules at %v, want %v", got, tt.wantY)
			}
			for i := range got {
				if !near(got[i], tt.wantY[i]) {
					t.Errorf("rules at %v, want %v", got, tt.wantY)
					break
				}
			}
		})
	}
}

func TestParagraphBorders_CountInBlockHeight(t *testing.T) {
	p := Paragraph{Runs: []Run{run("ab", 12)}, Props: ParagraphProperties{Borders: ParagraphBorders{
		Top:    BorderSide{Style: "single", WidthPt: 1, SpacePt: 2},
		Bottom: BorderSide{Style: "double", WidthPt: 1, SpacePt: 3},
	}}}
	got := blockHeight(&fakeRenderer{}, bodyOf(p), contentWidthPt)
	if want := 12*lineSpacing + 3 + 6; !near(got, want) {
		t.Errorf("blockHeight(bordered paragraph) = %v, want %v", got, want)
	}
}

func TestCharSpacing(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t,
		`<w:p><w:r><w:rPr><w:spacing w:val="60"/></w:rPr><w:t>ЗАЯВА</w:t></w:r>`+
			`<w:r><w:rPr><w:spacing w:val="-20"/></w:rPr><w:t>x</w:t></w:r></w:p>`))
	runs := doc.Body[0].Paragraph.Runs
	if runs[0].Props.CharSpacingPt != 3 || runs[1].Props.CharSpacingPt != -1 {
		t.Errorf("CharSpacingPt = %v, %v, want 3, -1", runs[0].Props.CharSpacingPt, runs[1].Props.CharSpacingPt)
	}

	spaced := Run{Text: "ЗАЯВА", Props: RunProperties{SizePt: 12, CharSpacingPt: 3}}
	if w := measureWords(&fakeRenderer{}, Paragraph{Runs: []Run{spaced}})[0].width; w != 5*6+5*3 {
		t.Errorf("measured width of spaced ЗАЯВА = %v, want %v", w, 5*6+5*3)
	}
}

func TestCharSpacing_FPDF(t *testing.T) {
	r := newFPDFRenderer(testGeometry.WidthPt, testGeometry.HeightPt, nil, nil)
	r.pdf.SetCompression(false)
	r.AddPage(testGeometry.WidthPt, testGeometry.HeightPt)
	r.SetFont(fonts.Serif, true, false, false, false, 14)
	plain := r.TextWidth("НАКАЗ")
	r.SetCharSpacing(3)
	if got := r.TextWidth("НАКАЗ"); !near(got, plain+15) {
		t.Errorf("TextWidth(НАКАЗ) with 3 pt spacing = %v, want %v", got, plain+15)
	}
	r.DrawText(marginPt, 120, "НАКАЗ")
	var buf bytes.Buffer
	if err := r.Output(&buf); err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("3.000 Tc")) || !bytes.Contains(buf.Bytes(), []byte("0 Tc")) {
		t.Errorf("PDF does not set and reset the character spacing (Tc)")
	}
}

func TestOpenReader(t *testing.T) {
	data, err := os.ReadFile(docxtest.Build(t, `<w:p><w:r><w:t>hello</w:t></w:r></w:p>`))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := OpenReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("OpenReader(valid docx) error = %v", err)
	}
	if got := doc.Body[0].Paragraph.Runs[0].Text; got != "hello" {
		t.Errorf("OpenReader(valid docx) text = %q, want %q", got, "hello")
	}

	if _, err := OpenReader(bytes.NewReader(nil), maxDocxSize+1); !errors.Is(err, ErrTooLarge) {
		t.Errorf("OpenReader(size %d) error = %v, want ErrTooLarge", maxDocxSize+1, err)
	}
	if _, err := OpenReader(strings.NewReader("not a zip"), 9); err == nil || errors.Is(err, ErrTooLarge) {
		t.Errorf("OpenReader(not a zip) error = %v, want a format error", err)
	}
}

// TestHyphenBreak covers where a line may wrap, the first line's own width
// included.
func TestHyphenBreak(t *testing.T) {
	tests := []struct {
		name  string
		runs  []Run
		width float64
		want  []string // each line's words joined
	}{
		{"after a hyphen between letters", []Run{run("інженеру-програмісту", 10)}, 60, []string{"інженеру-", "програмісту"}},
		{"across runs", []Run{run("інженеру-", 10), run("програмісту", 10)}, 60, []string{"інженеру-", "програмісту"}},
		{"fits: no break", []Run{run("інженеру-програмісту", 10)}, 200, []string{"інженеру-програмісту"}},
		{"not before a digit", []Run{run("2024-09-16", 10)}, 30, []string{"2024-09-16"}},
		{"not a leading hyphen", []Run{run("-abcdefgh", 10)}, 30, []string{"-abcdefgh"}},
		{"first-line indent narrows the first line", []Run{run("ab cd ef", 10)}, 39, []string{"ab", "cdef"}},
		{"hanging indent widens it", []Run{run("ab cd ef", 10)}, 20, []string{"abcd", "ef"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Paragraph{Runs: tt.runs}
			switch {
			case strings.HasPrefix(tt.name, "first-line"):
				p.Props.Indent.FirstLineOffsetPt = 15 // "ab cd" is 25 pt: it fits 39, not 39-15
			case strings.HasPrefix(tt.name, "hanging"):
				p.Props.Indent = Indent{LeftPt: 10, FirstLineOffsetPt: -10} // "ab cd" fits 20+10, not 20
			}
			var got []string
			for _, ln := range layoutParagraph(&fakeRenderer{}, p, tt.width) {
				var s strings.Builder
				for _, w := range ln.words {
					s.WriteString(w.text)
				}
				got = append(got, s.String())
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("lines = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPDF_EmbedsOnlyUsedFonts(t *testing.T) {
	doc := &Document{Body: bodyOf(Paragraph{Runs: []Run{{Text: "Наказ", Props: RunProperties{FontFamily: "Times New Roman", SizePt: 14}}}})}
	var buf bytes.Buffer
	if err := ConvertToPdf(doc).Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n := bytes.Count(buf.Bytes(), []byte("/FontFile2")); n != 1 {
		t.Errorf("PDF embeds %d fonts, want 1 (only Serif regular is drawn)", n)
	}
}

func TestPDF_FixedCreationDateIsReproducible(t *testing.T) {
	doc := &Document{Body: bodyOf(Paragraph{Runs: []Run{run("Наказ", 14), {Text: " bold", Props: RunProperties{Bold: true, SizePt: 14}}}})}
	date := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	write := func() []byte {
		var buf bytes.Buffer
		if err := ConvertToPdf(doc).WithCreationDate(date).Write(&buf); err != nil {
			t.Fatalf("Write: %v", err)
		}
		return buf.Bytes()
	}
	first := write()
	if !bytes.Contains(first, []byte("/CreationDate (D:20260102030405")) {
		t.Errorf("PDF lacks the fixed creation date")
	}
	for range 5 {
		if !bytes.Equal(first, write()) {
			t.Fatal("two writes with the same creation date differ")
		}
	}
}

func TestJustifiedFirstLineEndsAtTheRightEdge(t *testing.T) {
	for _, offset := range []float64{36, -36} {
		f := &fakeRenderer{}
		ln := layoutParagraph(f, para(AlignJustify, false, run("aa bb cc", 12)), 100)[0]
		drawLine(f, ln, AlignJustify, marginPt, 100, 100, false, offset, true)
		last := f.draws[len(f.draws)-1]
		if right := last.x + f.TextWidth(last.text); !near(right, marginPt+100) {
			t.Errorf("justified first line (offset %v) ends at x=%v, want %v", offset, right, marginPt+100)
		}
	}
}
