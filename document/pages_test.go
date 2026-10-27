package document

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// lines is n single-line paragraphs of 12 pt text, as body elements.
func lines(n int, text string) []BodyElement {
	return bodyOf(repeatParas(n, text)...)
}

func TestHeaderFooter_TallHeaderPushesTheBodyDown(t *testing.T) {
	f := &fakeRenderer{}
	// Five 14.4 pt lines from the 36 pt header offset end at 108 pt, past the
	// 72 pt top margin.
	sec := headerSection(lines(5, "HDR"), nil)
	ConvertToPdf(docWithSection(sec, para(AlignLeft, false, run("body", 12)))).render(f)

	if got, want := drawsOf(f, "body")[0].y, 36+5*12*lineSpacing+12; got != want {
		t.Errorf("body baseline y = %.2f, want %.2f (one ascent below the header's foot)", got, want)
	}
}

func TestHeaderFooter_ShortHeaderLeavesTheMargin(t *testing.T) {
	f := &fakeRenderer{}
	sec := headerSection(lines(1, "HDR"), nil)
	ConvertToPdf(docWithSection(sec, para(AlignLeft, false, run("body", 12)))).render(f)

	if got, want := drawsOf(f, "body")[0].y, marginPt+12; got != want {
		t.Errorf("body baseline y = %.2f, want %.2f (the top margin)", got, want)
	}
}

func TestHeaderFooter_TallFooterEndsTheBodyAboveIt(t *testing.T) {
	f := &fakeRenderer{}
	footer := lines(8, "FTR")
	sec := headerSection(nil, footer)
	doc := docWithSection(sec, repeatParas(linesPerFrame(), "x")...)
	ConvertToPdf(doc).render(f)

	ftrTop := testGeometry.HeightPt - 36 - 8*12*lineSpacing
	for _, d := range drawsOf(f, "x") {
		if d.page == 1 && d.y > ftrTop {
			t.Fatalf("body line at baseline %.2f on page 1 runs into the footer starting at %.2f", d.y, ftrTop)
		}
	}
	if f.page != 2 {
		t.Errorf("pages = %d, want 2 (a frame's worth of lines no longer fits above the footer)", f.page)
	}
}

func TestFields_NumPagesIsTheRealCount(t *testing.T) {
	numPages := Run{Field: FieldNumPages, Props: RunProperties{SizePt: 12}}
	page := Run{Field: FieldPage, Props: RunProperties{SizePt: 12}}
	footer := []BodyElement{{Table: &Table{
		ColumnWidths: []float64{200},
		Rows: []Row{{Cells: []Cell{{
			ColSpan:    1,
			Paragraphs: []Paragraph{{Runs: []Run{page, run(" of ", 12), numPages}}},
		}}}},
	}}}
	f := &fakeRenderer{}
	doc := docWithSection(headerSection(nil, footer), repeatParas(2*linesPerFrame()+1, "x")...)
	ConvertToPdf(doc).render(f)

	if f.page < 3 {
		t.Fatalf("pages = %d, want at least 3", f.page)
	}
	var got []string
	for _, d := range f.draws {
		if d.text != "x" && d.text != "of" {
			got = append(got, d.text)
		}
	}
	var want []string
	for p := 1; p <= f.page; p++ {
		want = append(want, strconv.Itoa(p), strconv.Itoa(f.page))
	}
	if !slices.Equal(got, want) {
		t.Errorf("footer cell fields = %q, want %q", got, want)
	}
}

func TestColumns_BalancedBeforeAContinuousBreak(t *testing.T) {
	body := append(lines(10, "x"), lines(1, "after")...)
	doc := &Document{Body: body, Geometry: testGeometry, Sections: []Section{
		{Geometry: testGeometry, Columns: twoColumns(), End: 10},
		{Geometry: testGeometry, Columns: []Column{{WidthPt: contentWidthPt}}, End: 11, Continuous: true},
	}}
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	perColumn := make(map[float64]int)
	for _, d := range drawsOf(f, "x") {
		perColumn[d.x]++
	}
	if len(perColumn) != 2 || perColumn[marginPt] != 5 {
		t.Errorf("lines per column (by x) = %v, want 5 and 5", perColumn)
	}
	lineH := 12 * lineSpacing
	if got, want := drawsOf(f, "after")[0].y, marginPt+5*lineH+12; math.Abs(got-want) > 1e-9 {
		t.Errorf("next section's baseline = %.2f, want %.2f (right below the balanced columns)", got, want)
	}
}

func TestColumns_NotBalancedWithoutAContinuousBreak(t *testing.T) {
	doc := docWithSection(Section{Columns: twoColumns()}, repeatParas(10, "x")...)
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	for _, d := range drawsOf(f, "x") {
		if d.x != marginPt {
			t.Fatalf("line drawn at x = %.1f; the last section's columns fill top to bottom", d.x)
		}
	}
}

func TestTable_VerticalMergeTallerThanAPageContinues(t *testing.T) {
	words := make([]string, 200) // about 160 fit the part on page 1, 60 more the one on page 2
	for i := range words {
		words[i] = "w" + strconv.Itoa(i)
	}
	merged := Cell{
		ColSpan:       1,
		VMerge:        VMergeRestart,
		TextDirection: TextDirectionBTLR,
		Paragraphs:    []Paragraph{para(AlignLeft, false, run(strings.Join(words, " "), 12))},
	}
	rows := make([]Row, 3)
	for i := range rows {
		rows[i] = Row{Cells: []Cell{{ColSpan: 1, VMerge: VMergeContinue}}, HeightPt: 300, HeightRule: RowHeightAtLeast}
	}
	rows[0].Cells[0] = merged
	tbl := Table{ColumnWidths: []float64{100}, Rows: rows}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: []BodyElement{{Table: &tbl}}}).render(f)

	seen := make(map[string]int)
	pages := make(map[int]bool)
	for _, d := range f.draws {
		seen[d.text]++
		pages[d.page] = true
	}
	for _, w := range words {
		if seen[w] != 1 {
			t.Errorf("%q drawn %d times, want once", w, seen[w])
		}
	}
	if !pages[1] || !pages[2] {
		t.Errorf("vertical text drawn on pages %v, want 1 and 2 (continued after the cut)", pages)
	}
}
