package document

import "testing"

// --- Auto column widths ---

func TestLayoutTable_AutoColumnsAreSizedFromTheirContent(t *testing.T) {
	// Two columns with no declared width: the fake renderer makes every glyph
	// half the font size wide, so "ab" wants 12 pt and "abcdefgh" 48 pt.
	tbl := Table{
		ColumnWidths: []float64{0, 0},
		Rows: []Row{mkRow(
			mkCell(1, VMergeNone, para(AlignLeft, false, run("ab", 12))),
			mkCell(1, VMergeNone, para(AlignLeft, false, run("abcdefgh", 12))),
		)},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 400, contentHeightPt)

	if got := tl.colWidths; got[0] != 12 || got[1] != 48 {
		t.Fatalf("auto widths = %.1f/%.1f, want 12/48 (each column's content width)", got[0], got[1])
	}
}

func TestLayoutTable_AutoColumnsScaleDownToTheAvailableWidth(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{0, 0},
		Rows: []Row{mkRow(
			mkCell(1, VMergeNone, para(AlignLeft, false, run("ab", 12))),       // wants 12
			mkCell(1, VMergeNone, para(AlignLeft, false, run("abcdefgh", 12))), // wants 48
		)},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 30, contentHeightPt) // half of the 60 pt they want

	if got := tl.colWidths; got[0] != 6 || got[1] != 24 {
		t.Fatalf("scaled widths = %.1f/%.1f, want 6/24 (halved, keeping their proportions)", got[0], got[1])
	}
}

func TestLayoutTable_AutoColumnSharesWhatAFixedOneLeaves(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{100, 0},
		Rows: []Row{mkRow(
			mkCell(1, VMergeNone, para(AlignLeft, false, run("x", 12))),
			mkCell(1, VMergeNone, para(AlignLeft, false, run("wide content here", 12))), // wants 102
		)},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 150, contentHeightPt) // 50 pt left for the auto column

	if tl.colWidths[0] != 100 {
		t.Errorf("fixed column = %.1f, want 100 (untouched)", tl.colWidths[0])
	}
	if tl.colWidths[1] != 50 {
		t.Errorf("auto column = %.1f, want 50 (what the fixed one leaves)", tl.colWidths[1])
	}
}

func TestLayoutTable_AutoColumnIsNeverNarrowerThanItsLongestWord(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{0, 0},
		Rows: []Row{mkRow(
			// "unbreakable" is 11 glyphs → 66 pt, which the column cannot go under.
			mkCell(1, VMergeNone, para(AlignLeft, false, run("an unbreakable word", 12))),
			mkCell(1, VMergeNone, para(AlignLeft, false, run("a b c d e f g h i j", 12))),
		)},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 100, contentHeightPt)

	if tl.colWidths[0] < 66 {
		t.Errorf("first column = %.1f, want at least 66 (its longest word must fit)", tl.colWidths[0])
	}
	if sum := tl.colWidths[0] + tl.colWidths[1]; sum > 100.01 {
		t.Errorf("columns total %.1f, want no more than the 100 pt available", sum)
	}
}

func TestLayoutTable_SpanningCellWidensTheColumnsItCovers(t *testing.T) {
	wide := mkCell(2, VMergeNone, para(AlignLeft, false, run("aaaaaaaaaaaaaaaaaaaa", 12))) // 120 pt
	tbl := Table{
		ColumnWidths: []float64{0, 0},
		Rows: []Row{
			mkRow(wide),
			mkRow(
				mkCell(1, VMergeNone, para(AlignLeft, false, run("ab", 12))),   // 12
				mkCell(1, VMergeNone, para(AlignLeft, false, run("abcd", 12))), // 24
			),
		},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 400, contentHeightPt)

	// On their own the columns would want 12 and 24 pt; the spanning cell needs
	// 120 across both, so the deficit is shared in proportion (a third, two
	// thirds) - the columns end up 40 and 80.
	if got := tl.colWidths; got[0] != 40 || got[1] != 80 {
		t.Fatalf("widths = %.1f/%.1f, want 40/80 (widened to hold the spanning cell)", got[0], got[1])
	}
}

func TestLayoutTable_FixedLayoutIgnoresTheContent(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{0, 0},
		FixedLayout:  true,
		Rows: []Row{mkRow(
			mkCell(1, VMergeNone, para(AlignLeft, false, run("ab", 12))),
			mkCell(1, VMergeNone, para(AlignLeft, false, run("abcdefgh", 12))),
		)},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 200, contentHeightPt)

	if got := tl.colWidths; got[0] != 100 || got[1] != 100 {
		t.Fatalf("fixed-layout widths = %.1f/%.1f, want 100/100 (an even share, not content-sized)", got[0], got[1])
	}
}

// --- The table's own width (w:tblW) ---

func TestLayoutTable_DeclaredWidthScalesTheColumns(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{50, 150}, // together 200, but the table declares 100
		WidthPt:      100,
		Rows:         []Row{mkRow(mkCell(1, VMergeNone), mkCell(1, VMergeNone))},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 400, contentHeightPt)

	if got := tl.colWidths; got[0] != 25 || got[1] != 75 {
		t.Fatalf("widths = %.1f/%.1f, want 25/75 (halved to the table's declared 100 pt)", got[0], got[1])
	}
}

func TestLayoutTable_PercentWidthOfTheAvailableRoom(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{10, 10},
		WidthPct:     50,
		Rows:         []Row{mkRow(mkCell(1, VMergeNone), mkCell(1, VMergeNone))},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, 400, contentHeightPt)

	if got := tl.width(); got != 200 {
		t.Fatalf("table width = %.1f, want 200 (half of the 400 pt available)", got)
	}
	if got := tl.colWidths; got[0] != 100 || got[1] != 100 {
		t.Errorf("widths = %.1f/%.1f, want 100/100 (scaled up in proportion)", got[0], got[1])
	}
}

// --- Percentage cell margins ---

func TestLayoutTable_PercentCellMarginIsOfTheTableWidth(t *testing.T) {
	cell := mkCell(1, VMergeNone, para(AlignLeft, false, run("hi", 12)))
	cell.Margins = CellMargins{LeftPct: 10} // a tenth of the table's width
	tbl := Table{ColumnWidths: []float64{200}, Rows: []Row{mkRow(cell)}}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	if want := marginPt + 20; f.draws[0].x != want { // 10% of 200 pt
		t.Fatalf("cell text x = %.2f, want %.2f (a 10%% left margin of the 200 pt table)", f.draws[0].x, want)
	}
}

// --- Floating tables (w:tblpPr) ---

func TestRenderTable_FloatingTableIsPlacedAndLeavesTheCursor(t *testing.T) {
	cell := mkCell(1, VMergeNone, para(AlignLeft, false, run("float", 12)))
	tbl := Table{
		ColumnWidths: []float64{100},
		Rows:         []Row{mkRow(cell)},
		Float:        &TableFloat{XPt: 20, YPt: 30, HorzAnchor: AnchorMargin, VertAnchor: AnchorMargin},
	}

	f := &fakeRenderer{}
	fl := testFlow(f)
	renderTable(fl, tbl)

	if want := testGeometry.MarginLeftPt + 20; f.draws[0].x != want {
		t.Errorf("floating cell x = %.2f, want %.2f (20 pt from the left margin)", f.draws[0].x, want)
	}
	if want := testGeometry.MarginTopPt + 30 + 12; f.draws[0].y != want { // + the line's ascent
		t.Errorf("floating cell y = %.2f, want %.2f (30 pt from the top margin)", f.draws[0].y, want)
	}
	if fl.y != testPage.originY {
		t.Errorf("cursor moved to %.2f: a floating table is out of the text flow and must leave it at %.2f",
			fl.y, testPage.originY)
	}
}

func TestRenderTable_FloatingTableCenteredOnThePage(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{100},
		Rows:         []Row{mkRow(mkCell(1, VMergeNone, para(AlignLeft, false, run("x", 12))))},
		Float:        &TableFloat{HorzAnchor: AnchorPage, XSpec: "center"},
	}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	if want := (testGeometry.WidthPt - 100) / 2; f.draws[0].x != want {
		t.Fatalf("centered floating table x = %.2f, want %.2f", f.draws[0].x, want)
	}
}

// --- Vertically merged cells stretch the rows they span ---

func TestLayoutTable_MergedCellGrowsTheRowsItSpans(t *testing.T) {
	// A tall merged cell over two short rows: the rows must stretch to hold it.
	tall := Cell{
		ColSpan: 1, VMerge: VMergeRestart,
		Paragraphs: []Paragraph{
			para(AlignLeft, false, run("one", 12)),
			para(AlignLeft, false, run("two", 12)),
			para(AlignLeft, false, run("three", 12)),
			para(AlignLeft, false, run("four", 12)),
		},
	}
	tbl := Table{
		ColumnWidths: []float64{100, 100},
		Rows: []Row{
			mkRow(tall, mkCell(1, VMergeNone, para(AlignLeft, false, run("a", 12)))),
			mkRow(Cell{ColSpan: 1, VMerge: VMergeContinue}, mkCell(1, VMergeNone, para(AlignLeft, false, run("b", 12)))),
		},
	}
	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

	want := 4 * 12 * lineSpacing // the merged cell's four lines
	if got := tl.mergedHeight(0, 0); got < want-0.01 {
		t.Fatalf("merged height = %.2f, want at least %.2f (the rows must stretch to its content)", got, want)
	}
	if tl.rows[0].height <= 0 || tl.rows[1].height <= 0 {
		t.Fatal("both spanned rows must keep a positive height")
	}
}

func TestLayoutTable_RestartCellDoesNotInflateItsFirstRow(t *testing.T) {
	// A cell merged down four rows, carrying one paragraph per spanned row (as
	// Word emits: the text plus trailing empty paragraphs), beside a column of
	// single-line cells. Every body row must come out the same height - the
	// merged cell's height belongs to the whole span, not piled onto row 0.
	restart := Cell{ColSpan: 1, VMerge: VMergeRestart, Paragraphs: []Paragraph{
		para(AlignLeft, false, run("x", 12)),
		para(AlignLeft, false, run("", 12)),
		para(AlignLeft, false, run("", 12)),
		para(AlignLeft, false, run("", 12)),
	}}
	cont := func() Cell { return Cell{ColSpan: 1, VMerge: VMergeContinue} }
	tbl := Table{ColumnWidths: []float64{100, 100}, Rows: []Row{
		mkRow(mkCell(1, VMergeNone, para(AlignLeft, false, run("a", 12))), restart),
		mkRow(mkCell(1, VMergeNone, para(AlignLeft, false, run("b", 12))), cont()),
		mkRow(mkCell(1, VMergeNone, para(AlignLeft, false, run("c", 12))), cont()),
		mkRow(mkCell(1, VMergeNone, para(AlignLeft, false, run("d", 12))), cont()),
	}}
	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

	h0 := tl.rows[0].height
	for i, r := range tl.rows {
		if r.height < h0-0.01 || r.height > h0+0.01 {
			t.Errorf("row %d height = %.2f, want %.2f (all merged-span rows equal)", i, r.height, h0)
		}
	}
}

func TestLayoutTable_MergedCellCannotGrowAFixedRow(t *testing.T) {
	tall := Cell{
		ColSpan: 1, VMerge: VMergeRestart,
		Paragraphs: []Paragraph{
			para(AlignLeft, false, run("one", 12)),
			para(AlignLeft, false, run("two", 12)),
			para(AlignLeft, false, run("three", 12)),
		},
	}
	fixed := mkRow(tall)
	fixed.HeightRule, fixed.HeightPt = RowHeightExact, 20
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{fixed}}

	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

	if tl.rows[0].height != 20 {
		t.Fatalf("row height = %.2f, want 20 (an exact height is not stretched by its content)", tl.rows[0].height)
	}
}

// --- Clipping a fixed-height row ---

func TestRenderTable_ExactRowClipsItsContent(t *testing.T) {
	cell := mkCell(1, VMergeNone,
		para(AlignLeft, false, run("one", 12)),
		para(AlignLeft, false, run("two", 12)),
		para(AlignLeft, false, run("three", 12)),
	)
	row := mkRow(cell)
	row.HeightRule, row.HeightPt = RowHeightExact, 20
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{row}}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	if len(f.clips) != 1 {
		t.Fatalf("clips = %d, want 1 (the exact-height row's cell)", len(f.clips))
	}
	c := f.clips[0]
	if c.x != marginPt || c.y != marginPt || c.w != 100 || c.h != 20 {
		t.Errorf("clip rect = (%.1f,%.1f %.1fx%.1f), want the cell's box (%.1f,%.1f 100x20)",
			c.x, c.y, c.w, c.h, marginPt, marginPt)
	}
	if c.from != 0 || c.to != len(f.draws) {
		t.Errorf("the cell's text must all be drawn inside the clip, got draws[%d:%d] of %d", c.from, c.to, len(f.draws))
	}
}

// --- Row height (w:trHeight) ---

func TestLayoutTable_RowHeightRules(t *testing.T) {
	oneLine := 12 * lineSpacing
	tests := []struct {
		name   string
		rule   RowHeightRule
		height float64
		want   float64
	}{
		{"auto", RowHeightAuto, 0, oneLine},
		{"atLeast, taller than the content", RowHeightAtLeast, 50, 50},
		{"atLeast, shorter than the content", RowHeightAtLeast, 5, oneLine},
		{"exact, even below the content", RowHeightExact, 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := mkRow(mkCell(1, VMergeNone, para(AlignLeft, false, run("hi", 12))))
			row.HeightRule, row.HeightPt = tt.rule, tt.height
			tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{row}}

			tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)
			if !approx(tl.rows[0].height, tt.want) {
				t.Fatalf("row height = %.2f, want %.2f (one line is %.2f)", tl.rows[0].height, tt.want, oneLine)
			}
		})
	}
}

// --- Vertical alignment (w:vAlign) ---

func TestRenderTable_VerticalAlignmentMovesTheContentDown(t *testing.T) {
	tests := []struct {
		name  string
		align VerticalAlign
		want  float64
	}{
		{"top", VAlignTop, 0},
		{"center", VAlignCenter, (60 - 12*lineSpacing) / 2},
		{"bottom", VAlignBottom, 60 - 12*lineSpacing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cell := mkCell(1, VMergeNone, para(AlignLeft, false, run("hi", 12)))
			cell.VAlign = tt.align
			row := mkRow(cell)
			row.HeightRule, row.HeightPt = RowHeightExact, 60
			tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{row}}

			f := &fakeRenderer{}
			renderTable(testFlow(f), tbl)

			if want := marginPt + tt.want + 12; f.draws[0].y != want { // + the line's ascent
				t.Fatalf("text baseline = %.2f, want %.2f", f.draws[0].y, want)
			}
		})
	}
}

// --- Inside grid lines ---

func TestRenderTable_InsideBordersOnlyBetweenCells(t *testing.T) {
	outer := BorderSide{Style: "single", WidthPt: 2, Color: "#000000"}
	inside := BorderSide{Style: "single", WidthPt: 0.5, Color: "#999999"}
	// A 2x1 grid whose cells were resolved as Word would: the shared edge takes
	// the inside line, the table's outline takes the outer one.
	top := mkCell(1, VMergeNone, para(AlignLeft, false, run("a", 12)))
	top.Borders = CellBorders{Top: outer, Left: outer, Right: outer, Bottom: inside}
	bottom := mkCell(1, VMergeNone, para(AlignLeft, false, run("b", 12)))
	bottom.Borders = CellBorders{Top: inside, Left: outer, Right: outer, Bottom: outer}
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{mkRow(top), mkRow(bottom)}}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	var thin int
	for _, s := range f.strokes {
		if s.widthPt == 0.5 {
			thin++
		}
	}
	if thin != 2 { // the shared edge, drawn by both cells
		t.Fatalf("thin (inside) strokes = %d, want 2 (the one shared edge, from each side)", thin)
	}
}

// --- Header row repetition ---

func TestRenderTable_HeaderRowsRepeatOnEveryPage(t *testing.T) {
	rows := []Row{{
		Header: true,
		Cells:  []Cell{mkCell(1, VMergeNone, para(AlignLeft, false, run("HEAD", 12)))},
	}}
	for range 100 {
		rows = append(rows, mkRow(mkCell(1, VMergeNone, para(AlignLeft, false, run("body", 12)))))
	}
	tbl := Table{ColumnWidths: []float64{100}, Rows: rows, HeaderRows: 1}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	if f.page < 1 {
		t.Fatal("expected the table to paginate")
	}
	heads := drawsOf(f, "HEAD")
	if len(heads) != f.page+1 {
		t.Fatalf("header drawn %d times across %d pages, want once per page", len(heads), f.page+1)
	}
	for _, h := range heads[1:] {
		if h.y != testPage.originY+12 { // baseline of the first line of a fresh frame
			t.Errorf("repeated header y = %.2f, want the top of the page (%.2f)", h.y, testPage.originY+12)
		}
	}
}

// --- Cell margins ---

func TestRenderTable_CellMarginsInsetTheText(t *testing.T) {
	cell := mkCell(1, VMergeNone, para(AlignLeft, false, run("hi", 12)))
	cell.Margins = CellMargins{TopPt: 10, BottomPt: 2, LeftPt: 8, RightPt: 4}
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{mkRow(cell)}}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	if want := marginPt + 8; f.draws[0].x != want {
		t.Errorf("cell text x = %.2f, want %.2f (the left margin)", f.draws[0].x, want)
	}
	if want := marginPt + 10 + 12; f.draws[0].y != want { // top margin, then the line's ascent
		t.Errorf("cell text y = %.2f, want %.2f (the top margin)", f.draws[0].y, want)
	}
}

// --- Vertical text ---

func TestRenderTable_VerticalCellRotatesItsText(t *testing.T) {
	cell := mkCell(1, VMergeNone, para(AlignLeft, false, run("up", 12)))
	cell.TextDirection = TextDirectionBTLR
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{mkRow(cell)}}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	if len(f.rotations) != 1 {
		t.Fatalf("rotations = %d, want 1 (the vertical cell)", len(f.rotations))
	}
	rot := f.rotations[0]
	rowHeight := minRowHeightPt // "up" is 12 pt long, so the row keeps its floor
	if rot.deg != 90 {
		t.Errorf("rotation = %.0f°, want 90 (bottom-to-top text)", rot.deg)
	}
	if rot.x != marginPt || rot.y != marginPt+rowHeight {
		t.Errorf("rotation origin = (%.2f, %.2f), want the cell's bottom-left corner (%.2f, %.2f)",
			rot.x, rot.y, marginPt, marginPt+rowHeight)
	}
	if rot.to != 1 || rot.from != 0 {
		t.Fatalf("the cell's text must be drawn inside the rotated block, got draws[%d:%d]", rot.from, rot.to)
	}
	// Inside the block the text is laid out horizontally from the rotation origin;
	// the transform is what stands it up on the page.
	if f.draws[0].x != marginPt {
		t.Errorf("rotated text x = %.2f, want the box origin %.2f", f.draws[0].x, marginPt)
	}
}

func TestLayoutTable_VerticalCellRowGrowsWithTheTextLength(t *testing.T) {
	long := "a text far longer than the row is tall"
	cell := mkCell(1, VMergeNone, para(AlignLeft, false, run(long, 12)))
	cell.TextDirection = TextDirectionTBRL
	tbl := Table{ColumnWidths: []float64{40}, Rows: []Row{mkRow(cell)}}

	tl := layoutTable(&fakeRenderer{}, tbl, contentWidthPt, contentHeightPt)

	// The text runs along the row's height, so the row is as tall as the text is
	// long (each glyph being half of the 12 pt size wide in the fake renderer).
	want := float64(len(long)) * 6
	if tl.rows[0].height != want {
		t.Fatalf("vertical row height = %.2f, want %.2f (the unwrapped text's length)", tl.rows[0].height, want)
	}
}

// --- Diagonal borders ---

func TestRenderTable_DiagonalBordersAreStroked(t *testing.T) {
	cell := mkCell(1, VMergeNone)
	cell.Borders = CellBorders{
		DiagDown: BorderSide{Style: "single", WidthPt: 1, Color: "#111111"},
		DiagUp:   BorderSide{Style: "single", WidthPt: 1, Color: "#222222"},
	}
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{mkRow(cell)}}

	f := &fakeRenderer{}
	renderTable(testFlow(f), tbl)

	if len(f.strokes) != 2 {
		t.Fatalf("strokes = %d, want 2 (the two diagonals; no sides declared)", len(f.strokes))
	}
	h := minRowHeightPt
	down, up := f.strokes[0], f.strokes[1]
	if down.x1 != marginPt || down.y1 != marginPt || down.x2 != marginPt+100 || down.y2 != marginPt+h {
		t.Errorf("tl2br diagonal = (%.1f,%.1f)-(%.1f,%.1f), want the cell's top-left to bottom-right",
			down.x1, down.y1, down.x2, down.y2)
	}
	if up.y1 != marginPt+h || up.y2 != marginPt {
		t.Errorf("tr2bl diagonal = (%.1f,%.1f)-(%.1f,%.1f), want the cell's bottom-left to top-right",
			up.x1, up.y1, up.x2, up.y2)
	}
}
