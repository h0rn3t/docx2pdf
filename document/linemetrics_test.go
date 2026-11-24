package document

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/h0rn3t/docx2pdf/document/fonts"
	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

// The line metrics Word lays a font's lines out by: the taller of the hhea and
// OS/2 win line heights, with the win descent below the baseline.
func TestFontLineMetrics(t *testing.T) {
	tests := []struct {
		name                string
		data                []byte
		wantAscent, wantAll float64 // in font units of a 2048 em
	}{
		// Times New Roman's clone: hhea 1825+443+87 beats win 1825+443.
		{"Liberation Serif", fonts.Bytes(fonts.Serif, false, false), 2355 - 443, 2355},
		// Calibri's clone: hhea and win agree on 2500, the win descent is 550.
		{"Carlito", fonts.Bytes(fonts.Carlito, false, false), 2500 - 550, 2500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asc, h, ok := fontLineMetrics(tt.data)
			if !ok || math.Abs(asc-tt.wantAscent/2048) > 1e-9 || math.Abs(h-tt.wantAll/2048) > 1e-9 {
				t.Errorf("fontLineMetrics = %v, %v, %v; want %v, %v, true", asc, h, ok, tt.wantAscent/2048, tt.wantAll/2048)
			}
		})
	}
	for _, bad := range [][]byte{nil, []byte("true"), make([]byte, 40)} {
		if _, _, ok := fontLineMetrics(bad); ok {
			t.Errorf("fontLineMetrics(%q) ok, want a malformed font rejected", bad)
		}
	}
}

// A line is as tall as its font's line height, times the paragraph's
// multiple; the text sits on the font's ascent and the extra goes below.
func TestLineHeightFromFontMetrics(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t,
		`<w:p><w:pPr><w:spacing w:line="276" w:lineRule="auto"/></w:pPr>`+
			`<w:r><w:rPr><w:sz w:val="28"/></w:rPr><w:t>one</w:t><w:br/><w:t>two</w:t></w:r></w:p>`+hrSectPr))
	f := &fakeRenderer{ascent: 0.9, lineHeight: 1.15}
	ConvertToPdf(doc).render(f)

	one, two := drawOf(t, f, "one"), drawOf(t, f, "two")
	if !approx(one.y, hrTop+0.9*14) {
		t.Errorf("first baseline at y=%v, want the ascent %v below the top", one.y, hrTop+0.9*14)
	}
	// 14pt × 1.15 (font) × 1.15 (w:line=276) = 18.515, in whole twips.
	if !approx(two.y-one.y, 18.5) {
		t.Errorf("line pitch = %v, want 18.5", two.y-one.y)
	}
}

// An empty paragraph is one line of its mark's font (w:pPr/w:rPr), not of a
// fixed default size.
func TestEmptyParagraphTakesItsMarkFont(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t,
		`<w:p><w:pPr><w:rPr><w:sz w:val="40"/></w:rPr></w:pPr></w:p>`+
			`<w:p><w:r><w:t>after</w:t></w:r></w:p>`+hrSectPr))
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	if got, want := drawOf(t, f, "after").y, hrTop+20*1.2+11; !approx(got, want) {
		t.Errorf("text after a 20pt empty paragraph at y=%v, want %v", got, want)
	}
}

// An empty paragraph that only ends a section before a continuous one takes
// no room, as in Word: the space after the paragraph before it and before
// the one after it collapse to the larger of the two.
func TestEmptySectionBreakBeforeAContinuousSection(t *testing.T) {
	const sect = `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="850" w:bottom="1134" w:left="1701"/></w:sectPr>`
	tests := []struct {
		name          string
		after, before int // twips
		wantGap       float64
	}{
		{"no spacing", 0, 0, 0},
		{"after only", 400, 0, 20},
		{"both: the larger wins", 200, 400, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := func(before, after int) string {
				return fmt.Sprintf(`<w:spacing w:before="%d" w:after="%d"/>`, before, after)
			}
			doc := openFixture(t, docxtest.Build(t,
				`<w:p><w:pPr>`+sp(0, tt.after)+`</w:pPr><w:r><w:t>one</w:t></w:r></w:p>`+
					`<w:p><w:pPr>`+sp(400, 400)+sect+`</w:pPr></w:p>`+
					`<w:p><w:pPr>`+sp(tt.before, 0)+`</w:pPr><w:r><w:t>two</w:t></w:r></w:p>`+
					`<w:sectPr><w:type w:val="continuous"/><w:pgSz w:w="11906" w:h="16838"/>`+
					`<w:pgMar w:top="1134" w:right="850" w:bottom="1134" w:left="1701"/></w:sectPr>`))
			f := &fakeRenderer{}
			ConvertToPdf(doc).render(f)

			if got, want := drawOf(t, f, "two").y-drawOf(t, f, "one").y, 11*1.2+tt.wantGap; !approx(got, want) {
				t.Errorf("baselines %v apart, want one line and the gap: %v", got, want)
			}
			if len(doc.Sections) != 2 || doc.Sections[0].End != 1 {
				t.Errorf("sections = %+v, want the first to end after %q", doc.Sections, "one")
			}
		})
	}
}

// Word hangs a left-aligned table out by its style's left cell margin, so the
// first column's text lines up with the indent; a margin set on the table
// itself does not move it (checked against Word's own output).
func TestTableHangsOutByItsStyleCellMargin(t *testing.T) {
	const borders = `<w:tblBorders><w:left w:val="single" w:sz="4"/></w:tblBorders>`
	tests := []struct {
		name, tblPr string
		wantTextX   float64
	}{
		{"default margin", ``, hrLeft},
		{"with an indent", `<w:tblInd w:w="720" w:type="dxa"/>`, hrLeft + 36},
		{"inline margin", `<w:tblCellMar><w:left w:w="300" w:type="dxa"/></w:tblCellMar>`, hrLeft + 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := renderBody(t, `<w:tbl><w:tblPr>`+tt.tblPr+borders+`</w:tblPr><w:tblGrid><w:gridCol w:w="3000"/></w:tblGrid>`+
				`<w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
			if got := drawOf(t, f, "cell").x; !approx(got, tt.wantTextX) {
				t.Errorf("cell text at x=%v, want %v", got, tt.wantTextX)
			}
		})
	}
}

// An empty paragraph closing a cell right after a nested table takes no room:
// Word requires it there, and draws nothing for it.
func TestEmptyParagraphAfterNestedTable(t *testing.T) {
	nested := `<w:tbl><w:tblGrid><w:gridCol w:w="1400"/></w:tblGrid><w:tr><w:tc><w:p><w:r><w:t>inner</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	f := renderBody(t, `<w:tbl><w:tblGrid><w:gridCol w:w="3000"/></w:tblGrid>`+
		`<w:tr><w:tc>`+nested+`<w:p/></w:tc></w:tr>`+
		`<w:tr><w:tc><w:p><w:r><w:t>next</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
	if got, want := drawOf(t, f, "next").y-drawOf(t, f, "inner").y, 11*1.2; !approx(got, want) {
		t.Errorf("next row's text %v below the nested table's, want one line, %v", got, want)
	}
}

// A nested table starts at its cell's text, and an auto-fit table wider than
// the room it has is narrowed to it, as Word lays out 04-complex.docx.
func TestNestedTableFitsItsCell(t *testing.T) {
	nested := `<w:tbl><w:tblGrid><w:gridCol w:w="2000"/><w:gridCol w:w="2000"/></w:tblGrid>` +
		`<w:tr><w:tc><w:p><w:r><w:t>left</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>right</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	f := renderBody(t, `<w:tbl><w:tblGrid><w:gridCol w:w="3000"/></w:tblGrid>`+
		`<w:tr><w:tc><w:p><w:r><w:t>outer</w:t></w:r></w:p>`+nested+`<w:p/></w:tc></w:tr></w:tbl>`)

	outer, left, right := drawOf(t, f, "outer"), drawOf(t, f, "left"), drawOf(t, f, "right")
	if !approx(left.x, outer.x+5.4) {
		t.Errorf("nested text at x=%v, want its own cell margin past the outer text, %v", left.x, outer.x+5.4)
	}
	// The outer cell's text is 150-10.8 pt wide: two columns of 69.6.
	if !approx(right.x-left.x, (150-10.8)/2) {
		t.Errorf("nested columns %v apart, want them narrowed to share the cell, %v", right.x-left.x, (150-10.8)/2)
	}
}

// A table row that does not fit breaks across pages at a line, as in Word,
// unless it is marked w:cantSplit: then it moves to the next page whole.
func TestTableRowBreaksAcrossPages(t *testing.T) {
	var lines strings.Builder
	for i := range 6 {
		fmt.Fprintf(&lines, `<w:p><w:r><w:t>l%d</w:t></w:r></w:p>`, i)
	}
	for _, tt := range []struct {
		name, trPr string
		wantL0Page int
	}{
		{"splits", ``, 1},
		{"cantSplit", `<w:trPr><w:cantSplit/></w:trPr>`, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// 685pt of space before puts the table where two lines still fit.
			f := renderBody(t, `<w:p><w:pPr><w:spacing w:before="13700"/></w:pPr><w:r><w:t>top</w:t></w:r></w:p>`+
				`<w:tbl><w:tblGrid><w:gridCol w:w="3000"/></w:tblGrid><w:tr>`+tt.trPr+`<w:tc>`+lines.String()+`</w:tc></w:tr></w:tbl>`)
			pages := pagesOf(f)
			if pages["l0"] != tt.wantL0Page || pages["l5"] != 2 {
				t.Errorf("l0 on page %d, l5 on page %d; want %d and 2", pages["l0"], pages["l5"], tt.wantL0Page)
			}
			if tt.wantL0Page == 1 && pages["l1"] != 1 {
				t.Errorf("l1 on page %d, want the lines that fit kept on page 1", pages["l1"])
			}
			if l2 := drawOf(t, f, "l2"); tt.wantL0Page == 1 && !approx(l2.y, hrTop+11) {
				t.Errorf("l2 at y=%v, want it to open page 2 at %v", l2.y, hrTop+11)
			}
		})
	}
}

// A multiple line spacing adds a share of the font's line height, not of an
// inline picture's: the picture is not stretched (LibreOffice's rule, and
// what Word draws).
func TestLineSpacingDoesNotScaleAPicture(t *testing.T) {
	img := &Image{Name: "p.png", Type: "PNG", WidthPt: 100, HeightPt: 100}
	p := Paragraph{
		Runs:  []Run{{Image: img, Props: RunProperties{SizePt: 10}}},
		Props: ParagraphProperties{Spacing: Spacing{LineRule: LineSpacingMultiple, LineValue: 1.5}},
	}
	lines := layoutParagraph(&fakeRenderer{}, p, 400)
	// The 100pt picture, sitting on the baseline with nothing below it as Word
	// draws it, + half a 12pt line.
	if want := 100 + 0.5*12; len(lines) != 1 || !approx(lines[0].height, want) {
		t.Errorf("line height = %v, want %v", lines[0].height, want)
	}
}

// A list marker is set in the paragraph mark's font, its level's formatting
// over it: a 14pt paragraph gets a 14pt number.
func TestListMarkerTakesTheParagraphFont(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="paragraph" w:styleId="Big"><w:rPr><w:sz w:val="28"/></w:rPr></w:style>`)
	numbering := docxtest.Numbering(`<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/></w:lvl></w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`)
	doc := openFixture(t, docxtest.BuildWith(t,
		`<w:p><w:pPr><w:pStyle w:val="Big"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles, "word/numbering.xml": numbering}))
	if got := doc.Body[0].Paragraph.Runs[0].Props.SizePt; got != 14 {
		t.Errorf("marker size = %v, want 14 (the paragraph's)", got)
	}
}

// Word does not add the space after a paragraph and before the next one: the
// larger of the two separates them (measured in Word; LibreOffice's
// PARA_SPACE_MAX for Word documents).
func TestParagraphSpacingCollapsesToTheLarger(t *testing.T) {
	tests := []struct {
		after, before int // twips
		wantGap       float64
	}{
		{200, 200, 10},
		{400, 200, 20},
		{200, 400, 20},
		{0, 200, 10},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("after %d before %d", tt.after, tt.before), func(t *testing.T) {
			f := renderBody(t, fmt.Sprintf(`<w:p><w:pPr><w:spacing w:after="%d"/></w:pPr><w:r><w:t>one</w:t></w:r></w:p>`+
				`<w:p><w:pPr><w:spacing w:before="%d"/></w:pPr><w:r><w:t>two</w:t></w:r></w:p>`, tt.after, tt.before))
			if got := drawOf(t, f, "two").y - drawOf(t, f, "one").y; !approx(got, 11*1.2+tt.wantGap) {
				t.Errorf("baselines %v apart, want a line and %v", got, tt.wantGap)
			}
		})
	}
}

// Without the symbol font itself, a line set in it still takes the height
// Word gives it: Symbol's own metrics, not its stand-in's.
func TestSymbolFontKeepsItsLineHeight(t *testing.T) {
	r := newFPDFRenderer(595, 842, nil, nil)
	r.SetFont("Symbol", false, false, false, false, 10)
	asc, h := r.FontMetrics()
	if !approx(asc, 10.054) || !approx(h, 12.251) {
		t.Errorf("Symbol 10pt metrics = %v, %v; want 10.054, 12.251", asc, h)
	}
}

// A cell keeps its content at least half a border's width inside the border:
// a row with zero top and bottom margins grows by its borders (LibreOffice's
// lcl_adjustBorderDistance; Word's rows measure the same).
func TestCellMarginsClearTheBorders(t *testing.T) {
	border := BorderSide{Style: "single", WidthPt: 0.5, Color: "#000000"}
	cell := mkCell(1, VMergeNone, para(AlignLeft, false, run("hi", 12)))
	cell.Borders = CellBorders{Top: border, Bottom: border, Left: border, Right: border}
	tl := layoutTable(&fakeRenderer{}, Table{ColumnWidths: []float64{100}, Rows: []Row{mkRow(cell)}}, contentWidthPt, contentHeightPt)
	if want := 12*lineSpacing + 0.5; !approx(tl.rows[0].height, want) {
		t.Errorf("row height = %v, want one line and two half borders, %v", tl.rows[0].height, want)
	}
}

// Word drops a paragraph's space before at the top of a page the text flowed
// or a section broke onto, though not at the top of the document.
func TestSpaceBeforeDroppedAtThePageTop(t *testing.T) {
	const sect = `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="850" w:bottom="1134" w:left="1701"/></w:sectPr>`
	f := renderBody(t, `<w:p><w:pPr><w:spacing w:before="400"/></w:pPr><w:r><w:t>first</w:t></w:r></w:p>`+
		`<w:p><w:pPr>`+sect+`</w:pPr></w:p>`+
		`<w:p><w:pPr><w:spacing w:before="400"/></w:pPr><w:r><w:t>second</w:t></w:r></w:p>`)
	if got := drawOf(t, f, "first").y; !approx(got, hrTop+20+11) {
		t.Errorf("first paragraph at y=%v, want its space before kept at the top of the document, %v", got, hrTop+20+11)
	}
	if got := drawOf(t, f, "second").y; !approx(got, hrTop+11) {
		t.Errorf("paragraph opening page 2 at y=%v, want its space before dropped, %v", got, hrTop+11)
	}
}

// A tab does not make its line taller: Word sizes the line "дата → підпис"
// of a 10pt label by its text, though the tab's run is 14pt.
func TestTabDoesNotRaiseTheLine(t *testing.T) {
	p := Paragraph{Runs: []Run{
		{Text: "date", Props: RunProperties{SizePt: 10}},
		{Tab: true, Props: RunProperties{SizePt: 14}},
		{Text: "sign", Props: RunProperties{SizePt: 10}},
	}}
	if lines := layoutParagraph(&fakeRenderer{}, p, 400); len(lines) != 1 || !approx(lines[0].height, 12) {
		t.Errorf("line height = %v, want the 10pt text's 12", lines[0].height)
	}
	onlyTab := Paragraph{Runs: []Run{{Tab: true, Props: RunProperties{SizePt: 14}}}}
	if lines := layoutParagraph(&fakeRenderer{}, onlyTab, 400); len(lines) != 1 || !approx(lines[0].height, 14*1.2) {
		t.Errorf("tab-only line height = %v, want its font's %v", lines[0].height, 14*1.2)
	}
}
