package document

import "testing"

// twoColumns is the default page with two 200 pt columns 20 pt apart.
func twoColumns() []Column {
	return []Column{{WidthPt: 200, SpaceAfterPt: 20}, {WidthPt: 200}}
}

// docWithSection wraps paragraphs into a Document whose single section is sec
// (its End is set to cover the whole body).
func docWithSection(sec Section, paras ...Paragraph) *Document {
	body := bodyOf(paras...)
	sec.End = len(body)
	if sec.Geometry.WidthPt == 0 {
		sec.Geometry = testGeometry
	}
	if len(sec.Columns) == 0 {
		sec.Columns = []Column{{WidthPt: contentWidthPt}}
	}
	return &Document{Body: body, Geometry: sec.Geometry, Sections: []Section{sec}}
}

func TestColumns_SeparatorRuleIsDrawnBetweenColumns(t *testing.T) {
	f := &fakeRenderer{}
	doc := docWithSection(Section{Columns: twoColumns(), Separator: true},
		para(AlignLeft, false, run("x", 12)))
	ConvertToPdf(doc).render(f)

	if len(f.strokes) != 1 {
		t.Fatalf("strokes = %d, want 1 (one rule between the two columns)", len(f.strokes))
	}
	s := f.strokes[0]
	// Centered in the 20 pt gap after the first 200 pt column, full column height.
	if wantX := marginPt + 200 + 10; s.x1 != wantX || s.x2 != wantX {
		t.Errorf("separator x = (%.1f,%.1f), want %.1f centered in the gap", s.x1, s.x2, wantX)
	}
	if s.y1 != testPage.originY || s.y2 != testPage.bottomLimit {
		t.Errorf("separator spans y %.1f..%.1f, want the full column %.1f..%.1f", s.y1, s.y2, testPage.originY, testPage.bottomLimit)
	}
	if s.color != "#000000" {
		t.Errorf("separator color = %q, want black", s.color)
	}
}

func TestColumns_NoSeparatorWhenNotRequested(t *testing.T) {
	f := &fakeRenderer{}
	doc := docWithSection(Section{Columns: twoColumns()}, para(AlignLeft, false, run("x", 12)))
	ConvertToPdf(doc).render(f)
	if len(f.strokes) != 0 {
		t.Fatalf("strokes = %d, want 0 without w:sep", len(f.strokes))
	}
}

// repeatParas is n identical single-word paragraphs, for filling a frame.
func repeatParas(n int, text string) []Paragraph {
	paras := make([]Paragraph, n)
	for i := range paras {
		paras[i] = para(AlignLeft, false, run(text, 12))
	}
	return paras
}

// linesPerFrame is how many 12 pt lines fit between the top margin and the
// bottom limit of the default page.
func linesPerFrame() int {
	h := 12 * lineSpacing
	return int((testPage.bottomLimit - testPage.originY) / h)
}

func TestColumns_ContentFlowsIntoTheNextColumnBeforeANewPage(t *testing.T) {
	f := &fakeRenderer{}
	n := linesPerFrame() + 5 // overflows the first column, still fits the second
	doc := docWithSection(Section{Columns: twoColumns()}, repeatParas(n, "x")...)
	ConvertToPdf(doc).render(f)

	if f.page != 1 {
		t.Fatalf("pages = %d, want 1 (the overflow belongs in the second column)", f.page)
	}
	if len(f.draws) != n {
		t.Fatalf("draws = %d, want %d", len(f.draws), n)
	}
	if got := f.draws[0].x; got != marginPt {
		t.Errorf("first column x = %.2f, want %.2f", got, marginPt)
	}
	// The second column starts one column width plus its gap to the right, and
	// its first line begins back at the top of the frame (drawn at its baseline,
	// one font size below the line's top).
	overflow := f.draws[linesPerFrame()]
	if want := marginPt + 200 + 20.0; overflow.x != want {
		t.Errorf("overflow x = %.2f, want %.2f (the second column)", overflow.x, want)
	}
	if want := testPage.originY + 12; overflow.y != want {
		t.Errorf("overflow y = %.2f, want %.2f (the top of the second column)", overflow.y, want)
	}
}

func TestColumns_FullLastColumnStartsANewPage(t *testing.T) {
	f := &fakeRenderer{}
	n := 2*linesPerFrame() + 3 // fills both columns and spills over
	doc := docWithSection(Section{Columns: twoColumns()}, repeatParas(n, "x")...)
	ConvertToPdf(doc).render(f)

	if f.page != 2 {
		t.Fatalf("pages = %d, want 2 (both columns full)", f.page)
	}
	last := f.draws[n-1]
	if last.page != 2 || last.x != marginPt {
		t.Fatalf("last draw = page %d at x %.2f, want page 2 back in the first column (x %.2f)", last.page, last.x, marginPt)
	}
}

func TestColumnBreak_MovesToTheNextColumn(t *testing.T) {
	f := &fakeRenderer{}
	second := para(AlignLeft, false, run("second", 12))
	second.Props.ColumnBreak = true
	doc := docWithSection(Section{Columns: twoColumns()},
		para(AlignLeft, false, run("first", 12)), second)
	ConvertToPdf(doc).render(f)

	if len(f.draws) != 2 {
		t.Fatalf("draws = %d, want 2", len(f.draws))
	}
	if want := marginPt + 200 + 20.0; f.draws[1].x != want {
		t.Fatalf("after a column break x = %.2f, want %.2f (the second column)", f.draws[1].x, want)
	}
	if f.draws[1].page != f.draws[0].page {
		t.Fatal("a column break must stay on the same page")
	}
}

func TestSections_EachSectionAddsPagesAtItsOwnSize(t *testing.T) {
	f := &fakeRenderer{}
	landscape := PageGeometry{
		WidthPt: testGeometry.HeightPt, HeightPt: testGeometry.WidthPt,
		MarginTopPt: 72, MarginRightPt: 72, MarginBottomPt: 72, MarginLeftPt: 72,
	}
	body := bodyOf(para(AlignLeft, false, run("portrait", 12)), para(AlignLeft, false, run("landscape", 12)))
	doc := &Document{
		Body:     body,
		Geometry: testGeometry,
		Sections: []Section{
			{Geometry: testGeometry, Columns: []Column{{WidthPt: contentWidthPt}}, End: 1},
			{Geometry: landscape, Columns: []Column{{WidthPt: landscape.WidthPt - 144}}, End: 2},
		},
	}
	ConvertToPdf(doc).render(f)

	if len(f.pages) != 2 {
		t.Fatalf("pages = %d, want 2 (a section break starts a new page)", len(f.pages))
	}
	if f.pages[0].widthPt != testGeometry.WidthPt {
		t.Errorf("page 1 width = %.2f, want %.2f", f.pages[0].widthPt, testGeometry.WidthPt)
	}
	if f.pages[1].widthPt != landscape.WidthPt || f.pages[1].heightPt != landscape.HeightPt {
		t.Errorf("page 2 = %.0f×%.0f, want the landscape section's %.0f×%.0f",
			f.pages[1].widthPt, f.pages[1].heightPt, landscape.WidthPt, landscape.HeightPt)
	}
}

func TestSections_ContinuousBreakStaysOnTheSamePage(t *testing.T) {
	f := &fakeRenderer{}
	body := bodyOf(para(AlignLeft, false, run("before", 12)), para(AlignLeft, false, run("after", 12)))
	doc := &Document{
		Body:     body,
		Geometry: testGeometry,
		Sections: []Section{
			{Geometry: testGeometry, Columns: []Column{{WidthPt: contentWidthPt}}, End: 1},
			{Geometry: testGeometry, Columns: twoColumns(), Continuous: true, End: 2},
		},
	}
	ConvertToPdf(doc).render(f)

	if f.page != 1 {
		t.Fatalf("pages = %d, want 1 (a continuous break resumes on the current page)", f.page)
	}
	if f.draws[1].y <= f.draws[0].y {
		t.Fatalf("the continuous section must resume below the cursor (y %.2f), got %.2f", f.draws[0].y, f.draws[1].y)
	}
}

// --- Headers and footers ---

func headerSection(header, footer []BodyElement) Section {
	return Section{
		Geometry:       testGeometry,
		Columns:        []Column{{WidthPt: contentWidthPt}},
		Header:         header,
		Footer:         footer,
		HeaderOffsetPt: 36,
		FooterOffsetPt: 36,
	}
}

func drawsOf(f *fakeRenderer, text string) []drawCall {
	var out []drawCall
	for _, d := range f.draws {
		if d.text == text {
			out = append(out, d)
		}
	}
	return out
}

func TestHeaderFooter_DrawnOnEveryPage(t *testing.T) {
	f := &fakeRenderer{}
	sec := headerSection(
		bodyOf(para(AlignLeft, false, run("HDR", 12))),
		bodyOf(para(AlignLeft, false, run("FTR", 12))),
	)
	doc := docWithSection(sec, repeatParas(2*linesPerFrame(), "x")...)
	ConvertToPdf(doc).render(f)

	if f.page != 2 {
		t.Fatalf("pages = %d, want 2", f.page)
	}
	if got := len(drawsOf(f, "HDR")); got != 2 {
		t.Errorf("header drawn %d times, want once per page (2)", got)
	}
	if got := len(drawsOf(f, "FTR")); got != 2 {
		t.Errorf("footer drawn %d times, want once per page (2)", got)
	}
}

func TestHeaderFooter_PositionedAtTheirOffsets(t *testing.T) {
	f := &fakeRenderer{}
	sec := headerSection(
		bodyOf(para(AlignLeft, false, run("HDR", 12))),
		bodyOf(para(AlignLeft, false, run("FTR", 12))),
	)
	ConvertToPdf(docWithSection(sec, para(AlignLeft, false, run("body", 12)))).render(f)

	hdr := drawsOf(f, "HDR")[0]
	// The header's first line sits at the header offset; DrawText positions text
	// by its baseline, one font size below the line's top.
	if want := 36 + 12.0; hdr.y != want {
		t.Errorf("header baseline y = %.2f, want %.2f (36 pt offset + one line's ascent)", hdr.y, want)
	}
	// The footer's single line ends at its distance from the bottom edge.
	ftr := drawsOf(f, "FTR")[0]
	lineH := 12 * lineSpacing
	if want := testGeometry.HeightPt - 36 - lineH + 12; ftr.y != want {
		t.Errorf("footer baseline y = %.2f, want %.2f (its foot 36 pt above the page bottom)", ftr.y, want)
	}
	if ftr.y <= hdr.y {
		t.Error("the footer must be drawn below the header")
	}
}

func TestHeaderFooter_TitlePageUsesTheFirstPageParts(t *testing.T) {
	f := &fakeRenderer{}
	sec := headerSection(bodyOf(para(AlignLeft, false, run("HDR", 12))), nil)
	sec.TitlePage = true
	sec.FirstHeader = bodyOf(para(AlignLeft, false, run("FIRST", 12)))
	doc := docWithSection(sec, repeatParas(2*linesPerFrame(), "x")...)
	ConvertToPdf(doc).render(f)

	first := drawsOf(f, "FIRST")
	if len(first) != 1 || first[0].page != 1 {
		t.Fatalf("first-page header drawn %d times (page %v), want once on page 1", len(first), first)
	}
	def := drawsOf(f, "HDR")
	if len(def) != 1 || def[0].page != 2 {
		t.Fatalf("default header drawn %d times, want only on page 2 (the title page uses its own)", len(def))
	}
}

// --- Run shading and inline images ---

func TestRunShading_FillsBehindTheText(t *testing.T) {
	f := &fakeRenderer{}
	shaded := Run{Text: "hi", Props: RunProperties{FontFamily: "Helvetica", SizePt: 12, Shading: "#FFFF00"}}
	ConvertToPdf(&Document{Body: bodyOf(Paragraph{Runs: []Run{shaded}})}).render(f)

	if len(f.fills) != 1 {
		t.Fatalf("fills = %d, want 1 (the run's background)", len(f.fills))
	}
	fill := f.fills[0]
	if fill.color != "#FFFF00" {
		t.Errorf("fill color = %q, want #FFFF00", fill.color)
	}
	if fill.x != f.draws[0].x {
		t.Errorf("fill x = %.2f, want the drawn text's x %.2f", fill.x, f.draws[0].x)
	}
	if fill.w != 12 { // "hi" is 2 glyphs at half the 12 pt size
		t.Errorf("fill width = %.2f, want 12 (the run's measured width)", fill.w)
	}
}

func TestParagraphShading_FillsTheIndentedLineBox(t *testing.T) {
	f := &fakeRenderer{}
	p := paraWithIndent(AlignLeft, Indent{LeftPt: 36, RightPt: 18}, run("hi", 12))
	p.Props.Shading = "#EEEEEE"
	ConvertToPdf(&Document{Body: bodyOf(p)}).render(f)

	if len(f.fills) != 1 {
		t.Fatalf("fills = %d, want 1", len(f.fills))
	}
	fill := f.fills[0]
	if want := marginPt + 36; fill.x != want {
		t.Errorf("fill x = %.2f, want %.2f (the paragraph's left indent)", fill.x, want)
	}
	if want := contentWidthPt - 36 - 18; fill.w != want {
		t.Errorf("fill width = %.2f, want %.2f (the indented text area)", fill.w, want)
	}
	if want := 12 * lineSpacing; !approx(fill.h, want) {
		t.Errorf("fill height = %.2f, want the line height %.2f", fill.h, want)
	}
}

func TestImage_DrawnOnTheBaselineAndSizesItsLine(t *testing.T) {
	f := &fakeRenderer{}
	img := &Image{Name: "word/media/image1.png", Type: "PNG", Data: []byte{1}, WidthPt: 40, HeightPt: 30}
	p := Paragraph{Runs: []Run{
		{Image: img, Props: RunProperties{FontFamily: "Helvetica", SizePt: 12}},
		{Text: " after", Props: RunProperties{FontFamily: "Helvetica", SizePt: 12}},
	}}
	ConvertToPdf(&Document{Body: bodyOf(p)}).render(f)

	if len(f.images) != 1 {
		t.Fatalf("images = %d, want 1", len(f.images))
	}
	drawn := f.images[0]
	if drawn.w != 40 || drawn.h != 30 {
		t.Errorf("image drawn %.0f×%.0f, want 40×30", drawn.w, drawn.h)
	}
	// The line is as tall as the picture, whose foot rests on the baseline; the
	// text that follows shares that baseline.
	if drawn.y != testPage.originY {
		t.Errorf("image top y = %.2f, want the line top %.2f", drawn.y, testPage.originY)
	}
	if got := f.draws[0].y; got != testPage.originY+30 {
		t.Errorf("text baseline = %.2f, want %.2f (the image's foot)", got, testPage.originY+30)
	}
}

func TestImage_ListMarkerTabAdvanceReservesTheHangingIndent(t *testing.T) {
	f := &fakeRenderer{}
	// A list marker with a tab suffix: "1." is 12 pt wide but must advance 18 pt,
	// so the item's text starts at the paragraph's left indent.
	marker := Run{Text: "1.", Props: RunProperties{FontFamily: "Helvetica", SizePt: 12}, MinWidthPt: 18}
	item := Run{Text: "item", Props: RunProperties{FontFamily: "Helvetica", SizePt: 12}}
	p := Paragraph{
		Runs:  []Run{marker, item},
		Props: ParagraphProperties{Indent: Indent{LeftPt: 36, FirstLineOffsetPt: -18}},
	}
	ConvertToPdf(&Document{Body: bodyOf(p)}).render(f)

	if len(f.draws) != 2 {
		t.Fatalf("draws = %d, want 2", len(f.draws))
	}
	if want := marginPt + 36 - 18; f.draws[0].x != want {
		t.Errorf("marker x = %.2f, want %.2f (the hanging indent)", f.draws[0].x, want)
	}
	if want := marginPt + 36.0; f.draws[1].x != want {
		t.Errorf("item text x = %.2f, want %.2f (the tab stop at the left indent)", f.draws[1].x, want)
	}
}
