package document

import (
	"testing"
)

func mkCell(colSpan int, vmerge VMergeState, paras ...Paragraph) Cell {
	return Cell{Paragraphs: paras, ColSpan: colSpan, VMerge: vmerge}
}

func mkRow(cells ...Cell) Row {
	return Row{Cells: cells}
}

func TestLayoutTable_ColumnOffsets(t *testing.T) {
	tbl := Table{ColumnWidths: []float64{50, 30, 20}}
	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

	want := []float64{0, 50, 80}
	for i, w := range want {
		if tl.colOffsets[i] != w {
			t.Errorf("colOffsets[%d] = %.2f, want %.2f", i, tl.colOffsets[i], w)
		}
	}
}

func TestLayoutTable_ColumnOffsetsIncludeTableIndent(t *testing.T) {
	tbl := Table{ColumnWidths: []float64{50, 30, 20}, IndentPt: 100}
	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

	want := []float64{100, 150, 180}
	for i, w := range want {
		if tl.colOffsets[i] != w {
			t.Errorf("colOffsets[%d] = %.2f, want %.2f", i, tl.colOffsets[i], w)
		}
	}
}

func TestLayoutTable_AlignmentShiftsColumns(t *testing.T) {
	tests := []struct {
		name  string
		align Alignment
		want  float64 // the first column's offset
	}{
		{"left keeps indent", AlignLeft, 100},
		{"center ignores indent", AlignCenter, (contentWidthPt - 100) / 2},
		{"right ignores indent", AlignRight, contentWidthPt - 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := Table{ColumnWidths: []float64{60, 40}, IndentPt: 100, Alignment: tt.align}
			tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

			if tl.colOffsets[0] != tt.want || tl.colOffsets[1] != tt.want+60 {
				t.Errorf("align %v: colOffsets = %v, want [%v %v]", tt.align, tl.colOffsets, tt.want, tt.want+60)
			}
			if got := tl.width(); got != tt.want+100 {
				t.Errorf("align %v: width() = %v, want %v", tt.align, got, tt.want+100)
			}
		})
	}
}

func TestRenderTable_CellsAtColumnPositions(t *testing.T) {
	f := &fakeRenderer{}
	tbl := Table{
		ColumnWidths: []float64{100, 200},
		Rows: []Row{
			mkRow(
				mkCell(1, VMergeNone, para(AlignLeft, false, run("A1", 12))),
				mkCell(1, VMergeNone, para(AlignLeft, false, run("B1", 12))),
			),
		},
	}
	renderTable(testFlow(f), tbl)

	if len(f.draws) != 2 {
		t.Fatalf("expected 2 draws, got %d", len(f.draws))
	}
	if want := marginPt; f.draws[0].x != want {
		t.Errorf("cell A1 x = %.2f, want %.2f", f.draws[0].x, want)
	}
	if want := marginPt + 100.0; f.draws[1].x != want {
		t.Errorf("cell B1 x = %.2f, want %.2f", f.draws[1].x, want)
	}
}

func TestLayoutTable_RowHeightFitsTallestCell(t *testing.T) {
	// Narrow column forces this cell to wrap into several lines; the other
	// cell is a single short word.
	tall := para(AlignLeft, false, run("aa bb cc dd ee", 12))
	short := para(AlignLeft, false, run("x", 12))
	tbl := Table{
		ColumnWidths: []float64{20, 100},
		Rows: []Row{
			mkRow(mkCell(1, VMergeNone, tall), mkCell(1, VMergeNone, short)),
		},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

	if got := len(tl.rows[0].cells[0].content.items[0].para.lines); got < 2 {
		t.Fatalf("expected the narrow cell to wrap into multiple lines, got %d", got)
	}
	shortBoxHeight := tl.rows[0].cells[1].content.height
	if tl.rows[0].height <= shortBoxHeight {
		t.Fatalf("row height %.2f should exceed the short cell's own box height %.2f", tl.rows[0].height, shortBoxHeight)
	}
}

func TestRenderTable_HorizontalMergeSpansCombinedWidthNoInteriorBorder(t *testing.T) {
	f := &fakeRenderer{}
	borders := CellBorders{Top: BorderSide{Style: "single", WidthPt: 1, Color: "#000000"}}
	tbl := Table{
		ColumnWidths: []float64{50, 50},
		Rows: []Row{
			mkRow(Cell{
				ColSpan:    2,
				Borders:    borders,
				Paragraphs: []Paragraph{para(AlignLeft, false, run("wide", 12))},
			}),
		},
	}
	renderTable(testFlow(f), tbl)

	if len(f.strokes) != 1 {
		t.Fatalf("expected exactly one border stroke (no interior border at the merge boundary), got %d", len(f.strokes))
	}
	if want := marginPt + 100; f.strokes[0].x2 != want {
		t.Fatalf("merged cell right edge = %.2f, want %.2f (spanning both columns)", f.strokes[0].x2, want)
	}
}

func TestRenderTable_VerticalMergeSpansCombinedHeightAndSkipsContinueDraw(t *testing.T) {
	restart := Cell{VMerge: VMergeRestart, ColSpan: 1,
		Paragraphs: []Paragraph{para(AlignLeft, false, run("top", 12))}}
	continued := Cell{VMerge: VMergeContinue, ColSpan: 1}
	tbl := Table{
		ColumnWidths: []float64{100},
		Rows:         []Row{mkRow(restart), mkRow(continued)},
	}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)
	if len(f.draws) != 1 {
		t.Fatalf("expected exactly one draw (the vMerge-continue cell must not redraw), got %d", len(f.draws))
	}

	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)
	got := tl.mergedHeight(0, 0)
	want := tl.rows[0].height + tl.rows[1].height
	if got != want {
		t.Fatalf("merged height = %.2f, want %.2f (sum of both rows)", got, want)
	}
}

func TestRenderTable_CellShadingFills(t *testing.T) {
	f := &fakeRenderer{}
	tbl := Table{
		ColumnWidths: []float64{100},
		Rows:         []Row{mkRow(Cell{ColSpan: 1, Shading: "#D9D9D9"})},
	}
	renderTable(testFlow(f), tbl)

	if len(f.fills) != 1 {
		t.Fatalf("expected one fill call, got %d", len(f.fills))
	}
	if f.fills[0].color != "#D9D9D9" {
		t.Fatalf("fill color = %q, want %q", f.fills[0].color, "#D9D9D9")
	}
	if f.fills[0].w != 100 {
		t.Fatalf("fill width = %.2f, want 100", f.fills[0].w)
	}
}

func TestRenderTable_BordersPerSide(t *testing.T) {
	f := &fakeRenderer{}
	borders := CellBorders{
		Top:    BorderSide{Style: "single", WidthPt: 1, Color: "#111111"},
		Bottom: BorderSide{Style: "double", WidthPt: 2, Color: "#222222"},
	}
	tbl := Table{
		ColumnWidths: []float64{100},
		Rows:         []Row{mkRow(Cell{ColSpan: 1, Borders: borders})},
	}
	renderTable(testFlow(f), tbl)

	if len(f.strokes) != 2 {
		t.Fatalf("expected 2 border strokes (top+bottom only; left/right undeclared), got %d", len(f.strokes))
	}
	if f.strokes[0].widthPt != 1 || f.strokes[0].color != "#111111" {
		t.Errorf("top border = %+v, want {width:1 color:#111111}", f.strokes[0])
	}
	if f.strokes[1].widthPt != 2 || f.strokes[1].color != "#222222" {
		t.Errorf("bottom border = %+v, want {width:2 color:#222222}", f.strokes[1])
	}
}

func TestRenderTable_PaginatesWithoutSplittingRows(t *testing.T) {
	f := &fakeRenderer{}
	var rows []Row
	for range 200 {
		rows = append(rows, mkRow(Cell{
			ColSpan:    1,
			Paragraphs: []Paragraph{para(AlignLeft, false, run("row", 12))},
		}))
	}
	tbl := Table{ColumnWidths: []float64{100}, Rows: rows}
	renderTable(testFlow(f), tbl)

	if f.page < 2 {
		t.Fatalf("expected the table to paginate across multiple pages, got %d", f.page)
	}
	if len(f.draws) != 200 {
		t.Fatalf("expected 200 draws (one per row, none split), got %d", len(f.draws))
	}
}

func TestRenderTable_CellParagraphIndentShiftsText(t *testing.T) {
	f := &fakeRenderer{}
	indented := Cell{
		ColSpan:    1,
		Paragraphs: []Paragraph{paraWithIndent(AlignLeft, Indent{LeftPt: 20}, run("hi", 12))},
	}
	tbl := Table{
		ColumnWidths: []float64{100},
		Rows:         []Row{mkRow(indented)},
	}
	renderTable(testFlow(f), tbl)

	if len(f.draws) != 1 {
		t.Fatalf("expected 1 draw, got %d", len(f.draws))
	}
	if want := marginPt + 20.0; f.draws[0].x != want {
		t.Fatalf("indented cell text x = %.2f, want %.2f", f.draws[0].x, want)
	}
}

func TestRenderTable_NestedTableIndentIsIndependentOfParent(t *testing.T) {
	f := &fakeRenderer{}
	inner := Table{
		IndentPt:     20,
		ColumnWidths: []float64{50},
		Rows: []Row{mkRow(Cell{
			ColSpan:    1,
			Paragraphs: []Paragraph{para(AlignLeft, false, run("inner", 12))},
		})},
	}
	outer := Table{
		IndentPt:     40,
		ColumnWidths: []float64{100},
		Rows: []Row{mkRow(Cell{
			ColSpan:    1,
			Paragraphs: []Paragraph{para(AlignLeft, false, run("outer", 12))},
			Nested:     &inner,
		})},
	}
	renderTable(testFlow(f), outer)

	if len(f.draws) != 2 {
		t.Fatalf("expected 2 draws (outer cell + nested table), got %d", len(f.draws))
	}
	wantOuterX := marginPt + 40.0
	if f.draws[0].x != wantOuterX {
		t.Fatalf("outer cell text x = %.2f, want %.2f (shifted by the outer table's own indent)", f.draws[0].x, wantOuterX)
	}
	wantInnerX := f.draws[0].x + 20
	if f.draws[1].x != wantInnerX {
		t.Fatalf("nested cell text x = %.2f, want %.2f (own indent, additional to and independent of the parent's)", f.draws[1].x, wantInnerX)
	}
}

func TestRenderTable_NestedTableRendersWithinParentCell(t *testing.T) {
	f := &fakeRenderer{}
	inner := Table{
		ColumnWidths: []float64{50},
		Rows: []Row{mkRow(Cell{
			ColSpan:    1,
			Paragraphs: []Paragraph{para(AlignLeft, false, run("inner", 12))},
		})},
	}
	outer := Table{
		ColumnWidths: []float64{100},
		Rows: []Row{mkRow(Cell{
			ColSpan:    1,
			Paragraphs: []Paragraph{para(AlignLeft, false, run("outer", 12))},
			Nested:     &inner,
		})},
	}
	renderTable(testFlow(f), outer)

	if len(f.draws) != 2 {
		t.Fatalf("expected 2 draws (outer cell + nested table), got %d", len(f.draws))
	}
	if f.draws[0].text != "outer" || f.draws[1].text != "inner" {
		t.Fatalf("draw text = %q, %q, want \"outer\", \"inner\"", f.draws[0].text, f.draws[1].text)
	}
	// The nested table sits inside the outer cell's content area, so - with these
	// hand-built cells declaring no margins - its text starts at the same x as the
	// outer cell's own.
	if want := f.draws[0].x; f.draws[1].x != want {
		t.Fatalf("nested cell x = %.2f, want %.2f (the outer cell's content edge)", f.draws[1].x, want)
	}
	if f.draws[1].y <= f.draws[0].y {
		t.Fatalf("expected the nested table drawn below the outer cell's own text (y=%.2f)", f.draws[0].y)
	}
}
