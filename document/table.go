package document

import "math"

// paraBox is one cell paragraph's wrapped lines plus its alignment, indent,
// spacing and shading (drawLine needs them all, but a cell's draw pass happens
// after its row's cells are measured, so they're carried from layout time to
// then).
type paraBox struct {
	lines     []line
	align     Alignment
	indent    Indent
	spacing   Spacing
	shading   string
	tabs      []TabStop
	bookmarks []string
}

// cellContent is a cell's laid-out paragraphs and nested tables in document
// order, plus the space they need: height along the cell's stacking direction,
// and length (the widest unwrapped line) - which is what a vertical cell needs,
// its text running along the row's height.
type cellContent struct {
	items  []cellItem
	height float64
	length float64
}

// cellItem is one laid-out block of a cell: exactly one field is non-nil.
type cellItem struct {
	para  *paraBox
	table *tableLayout
}

// blocks is the cell's content in document order (see Cell.Content).
func (c Cell) blocks() []BodyElement {
	if len(c.Content) > 0 {
		return c.Content
	}
	out := make([]BodyElement, 0, len(c.Paragraphs)+1)
	for i := range c.Paragraphs {
		out = append(out, BodyElement{Paragraph: &c.Paragraphs[i]})
	}
	if c.Nested != nil {
		out = append(out, BodyElement{Table: c.Nested})
	}
	return out
}

// cellLayout pairs a cell with its laid-out content, its starting column index
// (needed to look up vertical-merge continuations in later rows) and its margins
// resolved to points (a percentage margin needs the table's width, which is only
// known once the columns are).
type cellLayout struct {
	cell    Cell
	col     int
	margins CellMargins
	content cellContent
}

// rowLayout is one table row: its resolved height, whether that height is fixed
// (w:hRule="exact", so the row may neither grow nor let its content spill out)
// and its cells' layouts.
type rowLayout struct {
	height float64
	fixed  bool
	cells  []cellLayout
}

// tableLayout is a fully measured table, ready to draw at any x/y origin:
// column offsets and row heights are relative, not absolute page coordinates.
type tableLayout struct {
	colWidths   []float64
	colOffsets  []float64 // colOffsets[i] = sum(colWidths[:i])
	rows        []rowLayout
	headerRows  int
	totalHeight float64
}

// width is how wide the table draws from its frame's origin: its own indent plus
// every column.
func (tl tableLayout) width() float64 {
	var w float64
	if len(tl.colOffsets) > 0 {
		w = tl.colOffsets[0] // the table's indent
	}
	for _, c := range tl.colWidths {
		w += c
	}
	return w
}

// layoutTable measures every row and cell of t against the width available to
// it (the page column for a top-level table, the parent cell's content box for a
// nested one). Column widths that the document leaves open are resolved from the
// content first (Word's auto-fit); column x-offsets then follow from t.IndentPt
// (or, for a centered or right-aligned table, from its alignment), so every cell - drawn via x0 + colOffsets[i] - picks up the table's own
// indent, relative to whatever frame x0 is measured from. Each row's height
// comes from its tallest non-continuation cell, floored at minRowHeightPt, and a
// vertically merged cell then stretches the rows it spans if it still needs more
// room.
func layoutTable(r renderer, t Table, availWidth, availHeight float64) tableLayout {
	widths := resolveTableWidths(r, t, availWidth)
	// A percentage cell margin is a percentage of the table's width, which is
	// only settled now that the columns are.
	var tableWidth float64
	for _, w := range widths {
		tableWidth += w
	}
	// A centered or right-aligned table ignores its indent, as in Word; a
	// floating one is placed by its own position instead.
	start := t.IndentPt
	if t.Float == nil {
		switch t.Alignment {
		case AlignCenter:
			start = math.Max((availWidth-tableWidth)/2, 0)
		case AlignRight:
			start = math.Max(availWidth-tableWidth, 0)
		case AlignLeft, AlignJustify:
			// Starts at its indent.
		}
	}
	offsets := make([]float64, len(widths))
	acc := start
	for i, w := range widths {
		offsets[i] = acc
		acc += w
	}
	tl := tableLayout{colWidths: widths, colOffsets: offsets, headerRows: t.HeaderRows}

	for _, row := range t.Rows {
		col := 0
		rl := rowLayout{height: minRowHeightPt, fixed: row.HeightRule == RowHeightExact}
		// Vertical text runs along the row's height, so that is what it wraps at:
		// a declared height when the row has one, else whatever room the frame
		// still offers.
		wrapLen := availHeight
		if row.HeightRule != RowHeightAuto && row.HeightPt > 0 {
			wrapLen = row.HeightPt
		}
		for _, cell := range row.Cells {
			span := colSpan(cell)
			width := spanWidth(widths, col, span)
			margins := cell.Margins.resolve(tableWidth)

			var content cellContent
			if cell.VMerge != VMergeContinue {
				content = layoutCellContent(r, cell, margins, width, wrapLen, availHeight)
				// A restart cell's content spans every row it merges, so its height is
				// applied by growMergedRows across the whole span. Piling it onto the
				// starting row instead would leave the first row tall and the merged
				// rows below it short (uneven).
				if cell.VMerge != VMergeRestart {
					if h := cellHeightNeeded(cell, margins, content); h > rl.height {
						rl.height = h
					}
				}
			}
			rl.cells = append(rl.cells, cellLayout{cell: cell, col: col, margins: margins, content: content})
			col += span
		}
		switch row.HeightRule {
		case RowHeightExact: // fixed, even when the content needs more
			rl.height = row.HeightPt
		case RowHeightAtLeast:
			rl.height = math.Max(rl.height, row.HeightPt)
		}
		tl.rows = append(tl.rows, rl)
	}
	growMergedRows(&tl)
	return tl
}

// growMergedRows lets a vertically merged cell whose content is taller than the
// rows it spans stretch them: the shortfall is shared among the rows that may
// grow (an exact-height row may not), in proportion to their heights. It also
// (re)computes the table's total height.
func growMergedRows(tl *tableLayout) {
	for ri := range tl.rows {
		for _, cl := range tl.rows[ri].cells {
			if cl.cell.VMerge != VMergeRestart {
				continue
			}
			last := tl.mergeEnd(ri, cl.col) - 1
			var have, flexible float64
			var grow []int
			for rj := ri; rj <= last; rj++ {
				have += tl.rows[rj].height
				if !tl.rows[rj].fixed {
					grow = append(grow, rj)
					flexible += tl.rows[rj].height
				}
			}
			deficit := cellHeightNeeded(cl.cell, cl.margins, cl.content) - have
			if deficit <= 0 || len(grow) == 0 {
				continue
			}
			for _, rj := range grow {
				if flexible <= 0 { // nothing to be proportional to: share it evenly
					tl.rows[rj].height += deficit / float64(len(grow))
					continue
				}
				tl.rows[rj].height += deficit * tl.rows[rj].height / flexible
			}
		}
	}
	tl.totalHeight = 0
	for _, rl := range tl.rows {
		tl.totalHeight += rl.height
	}
}

// cellHeightNeeded is how tall a row must be to hold the cell. A vertical cell's
// text runs along the row's height, so what it needs is the length of its
// longest line, padded by the margins at the ends of that text.
func cellHeightNeeded(cell Cell, m CellMargins, content cellContent) float64 {
	if cell.TextDirection != TextDirectionHorizontal {
		return content.length + m.LeftPt + m.RightPt
	}
	return content.height + m.TopPt + m.BottomPt
}

// layoutCellContent lays out cell's paragraphs and nested tables, stacked in
// document order, inside the cell's width minus its margins. A vertical cell's
// lines run along the row's height instead, so they are wrapped at wrapLen (the
// row's declared height, or the room the frame has left) rather than at the
// cell's width.
func layoutCellContent(r renderer, cell Cell, m CellMargins, width, wrapLen, availHeight float64) cellContent {
	inner := width - m.LeftPt - m.RightPt
	if cell.TextDirection != TextDirectionHorizontal {
		inner = wrapLen - m.LeftPt - m.RightPt
	}

	var cc cellContent
	for _, el := range cell.blocks() {
		if el.Table != nil {
			nt := layoutTable(r, *el.Table, inner, availHeight)
			cc.items = append(cc.items, cellItem{table: &nt})
			cc.height += nt.totalHeight
			continue
		}
		p := *el.Paragraph
		innerWidth := inner - p.Props.Indent.LeftPt - p.Props.Indent.RightPt
		lines := layoutParagraph(r, p, innerWidth)
		cc.items = append(cc.items, cellItem{para: &paraBox{
			lines:     lines,
			align:     p.Props.Alignment,
			indent:    p.Props.Indent,
			spacing:   p.Props.Spacing,
			shading:   p.Props.Shading,
			tabs:      p.Props.Tabs,
			bookmarks: p.Bookmarks,
		}})
		if len(lines) == 0 {
			cc.height += lineHeightFor(defaultRenderSizePt, 0, p.Props.Spacing)
			continue
		}
		for _, ln := range lines {
			cc.height += ln.height
			cc.length = math.Max(cc.length, ln.natural+p.Props.Indent.LeftPt+p.Props.Indent.RightPt)
		}
	}
	return cc
}

// --- Column widths ---

// resolveTableWidths turns the table's declared column widths into final ones:
// a declared width stands, a percentage width is taken of the table's width, and
// a column with neither is sized from the content of the cells in it (Word's
// auto-fit). Auto columns share whatever the declared ones leave: each is
// guaranteed at least its longest unbreakable word, and the rest of the room is
// handed out in proportion to how much wider each still wants to be. A fixed
// layout (w:tblLayout) never measures content at all - undeclared columns simply
// split what is left evenly. When the table declares a width of its own
// (w:tblW), the columns end up scaled to exactly that; otherwise the table is as
// wide as its columns turn out to be, up to the room it has.
func resolveTableWidths(r renderer, t Table, availWidth float64) []float64 {
	widths := make([]float64, len(t.ColumnWidths))
	copy(widths, t.ColumnWidths)

	// room is what the columns have to share: the table's own declared width when
	// it has one, else whatever is left of the frame beside its indent.
	room := math.Max(availWidth-t.IndentPt, 0)
	target := tableTargetWidth(t, availWidth)
	if target > 0 {
		room = target
	}

	var auto []int
	var declared float64
	for i := range widths {
		if widths[i] <= 0 && i < len(t.ColumnPercents) && t.ColumnPercents[i] > 0 {
			widths[i] = room * t.ColumnPercents[i] / 100
		}
		if widths[i] > 0 {
			declared += widths[i]
		} else {
			auto = append(auto, i)
		}
	}
	remaining := math.Max(room-declared, 0)
	shareEvenly := func() {
		for _, i := range auto {
			widths[i] = remaining / float64(len(auto))
		}
	}

	switch {
	case len(auto) == 0: // nothing to size
	case t.FixedLayout:
		shareEvenly()
	default:
		natural, minimal := columnDemands(r, t, len(widths))
		var want, need float64
		for _, i := range auto {
			want += natural[i]
			need += minimal[i]
		}
		switch {
		case want <= 0: // nothing to measure (empty cells)
			shareEvenly()
		case want <= remaining: // everything fits at its natural width
			for _, i := range auto {
				widths[i] = natural[i]
			}
		case need >= remaining: // not even the longest words fit: scale them together
			for _, i := range auto {
				widths[i] = minimal[i] * remaining / need
			}
		default: // every column gets its longest word, then shares what is left over
			extra, slack := remaining-need, want-need
			for _, i := range auto {
				widths[i] = minimal[i] + (natural[i]-minimal[i])*extra/slack
			}
		}
	}
	if target > 0 {
		scaleToWidth(widths, target)
	}
	return widths
}

// tableTargetWidth is the width the table declares for itself (w:tblW): a
// percentage of the room it has, or a fixed size. Zero when it declares none
// ("auto"), in which case the columns decide how wide it is.
func tableTargetWidth(t Table, availWidth float64) float64 {
	switch {
	case t.WidthPct > 0:
		return math.Max(availWidth-t.IndentPt, 0) * t.WidthPct / 100
	case t.WidthPt > 0:
		return t.WidthPt
	default:
		return 0
	}
}

// scaleToWidth stretches or shrinks the columns so that together they are
// exactly target wide.
func scaleToWidth(widths []float64, target float64) {
	var sum float64
	for _, w := range widths {
		sum += w
	}
	if sum <= 0 {
		return
	}
	for i := range widths {
		widths[i] *= target / sum
	}
}

// columnDemands is what each column's content asks for: naturally (its widest
// unwrapped line) and at minimum (its longest unbreakable word). A cell spanning
// several columns can't be charged to one of them, so it only widens the columns
// it covers when they are together narrower than it needs - and then in
// proportion to what they already ask for.
func columnDemands(r renderer, t Table, n int) (natural, minimal []float64) {
	natural, minimal = make([]float64, n), make([]float64, n)
	type spanning struct {
		col, span        int
		natural, minimal float64
	}
	var spans []spanning

	for _, row := range t.Rows {
		col := 0
		for _, cell := range row.Cells {
			span := colSpan(cell)
			nat, min := cellDemands(r, cell)
			switch {
			case span == 1 && col < n:
				natural[col] = math.Max(natural[col], nat)
				minimal[col] = math.Max(minimal[col], min)
			case span > 1 && col+span <= n:
				spans = append(spans, spanning{col: col, span: span, natural: nat, minimal: min})
			}
			col += span
		}
	}
	for _, s := range spans {
		spreadDemand(natural, s.col, s.span, s.natural)
		spreadDemand(minimal, s.col, s.span, s.minimal)
	}
	return natural, minimal
}

// spreadDemand widens the columns [col, col+span) until they together hold need.
func spreadDemand(demand []float64, col, span int, need float64) {
	var have float64
	for i := col; i < col+span; i++ {
		have += demand[i]
	}
	if have >= need {
		return
	}
	deficit := need - have
	for i := col; i < col+span; i++ {
		if have <= 0 { // nothing to be proportional to: split evenly
			demand[i] += deficit / float64(span)
			continue
		}
		demand[i] += deficit * demand[i] / have
	}
}

// cellDemands is how wide a cell wants to be, naturally and at minimum. A
// vertical cell's text runs down the row rather than across it, so what it asks
// of its column is the thickness of its stacked lines, which cannot be reduced.
func cellDemands(r renderer, cell Cell) (natural, minimal float64) {
	if cell.TextDirection != TextDirectionHorizontal {
		var thickness float64
		for _, p := range cell.Paragraphs {
			for _, ln := range layoutParagraph(r, p, math.Inf(1)) {
				thickness += ln.height
			}
		}
		w := thickness + cell.Margins.TopPt + cell.Margins.BottomPt
		return w, w
	}
	for _, p := range cell.Paragraphs {
		ind := p.Props.Indent.LeftPt + p.Props.Indent.RightPt
		for _, ln := range layoutParagraph(r, p, math.Inf(1)) {
			natural = math.Max(natural, ln.natural+ind)
		}
		// Laying the paragraph out at zero width puts every word on a line of its
		// own, so the widest of those lines is its longest unbreakable word.
		for _, ln := range layoutParagraph(r, p, 0) {
			minimal = math.Max(minimal, ln.natural+ind)
		}
	}
	m := cell.Margins.LeftPt + cell.Margins.RightPt
	return natural + m, minimal + m
}

// cellAt returns row's cell starting at column col, if any.
func (rl rowLayout) cellAt(col int) (cellLayout, bool) {
	for _, cl := range rl.cells {
		if cl.col == col {
			return cl, true
		}
	}
	return cellLayout{}, false
}

// mergeEnd returns the row index just past the vertical merge that the cell at
// row ri, column col starts: ri+1 plus every immediately-following row that
// continues the merge at that column.
func (tl tableLayout) mergeEnd(ri, col int) int {
	end := ri + 1
	for end < len(tl.rows) {
		cl, ok := tl.rows[end].cellAt(col)
		if !ok || cl.cell.VMerge != VMergeContinue {
			break
		}
		end++
	}
	return end
}

// mergeStart returns the row of the restart cell whose merge the continuation
// cell at row ri, column col belongs to.
func (tl tableLayout) mergeStart(ri, col int) (int, bool) {
	for rj := ri - 1; rj >= 0; rj-- {
		cl, ok := tl.rows[rj].cellAt(col)
		if !ok {
			return 0, false
		}
		switch cl.cell.VMerge {
		case VMergeRestart:
			return rj, true
		case VMergeNone:
			return 0, false
		case VMergeContinue: // keep walking up
		}
	}
	return 0, false
}

// blockEnd returns the row index just past the rows that must share a page with
// row ri: every row a vertical merge starting in the block reaches.
func (tl tableLayout) blockEnd(ri int) int {
	end := ri + 1
	for k := ri; k < end; k++ {
		for _, cl := range tl.rows[k].cells {
			if cl.cell.VMerge == VMergeRestart {
				end = max(end, tl.mergeEnd(k, cl.col))
			}
		}
	}
	return end
}

// rowsHeight is the total height of rows [from, to).
func (tl tableLayout) rowsHeight(from, to int) float64 {
	var h float64
	for _, rl := range tl.rows[from:to] {
		h += rl.height
	}
	return h
}

// mergedHeight returns the total height a vMerge-restart cell at row ri,
// column col draws across: its own row plus every row continuing the merge.
func (tl tableLayout) mergedHeight(ri, col int) float64 {
	return tl.rowsHeight(ri, tl.mergeEnd(ri, col))
}

func colSpan(cell Cell) int {
	if cell.ColSpan < 1 {
		return 1
	}
	return cell.ColSpan
}

func spanWidth(colWidths []float64, col, span int) float64 {
	var w float64
	for i := col; i < col+span && i < len(colWidths); i++ {
		w += colWidths[i]
	}
	return w
}

// renderTable lays out t and draws it down the flow's current column, moving to
// the next column or page before any row that would cross the bottom margin (a
// row is never split). Rows joined by a vertical merge move on together when
// they fit a fresh frame; a merge taller than that is cut at the bottom margin
// and its box carries on at the top of the next frame. The table's leading
// header rows (w:tblHeader) are redrawn at the top of every frame the table
// continues into. A floating table (w:tblpPr) is drawn at its own position
// instead and leaves the cursor where it was, since it is not part of the text
// flow.
func renderTable(f *flow, t Table) {
	fr := f.frame()
	tl := layoutTable(f.r, t, fr.contentWidth, fr.bottomLimit-fr.originY)
	// A floating table taller than a whole frame cannot be placed anywhere, so
	// it flows - and paginates - like any other.
	if t.Float != nil && tl.totalHeight <= f.frame().bottomLimit-f.top {
		x, y := floatOrigin(f, *t.Float, tl.width(), tl.totalHeight)
		if t.Float.VertAnchor == AnchorText && y+tl.totalHeight > f.frame().bottomLimit && !f.atTop {
			// Word moves a floating table that does not fit on to the next page
			// whole rather than splitting it.
			f.breakFrame()
			x, y = floatOrigin(f, *t.Float, tl.width(), tl.totalHeight)
		}
		drawTableRows(f.r, tl, x, y)
		f.addFloat(*t.Float, x, y, tl.width(), tl.totalHeight)
		return
	}
	frameStart := -1 // the first row drawn on a frame the table broke onto
	drawn := make(map[[2]int]float64)
	vertical := make(map[[2]int]*verticalCut)
	nextFrame := func(ri int) {
		f.breakFrame()
		if ri >= tl.headerRows { // the header rows themselves are not repeated above themselves
			drawHeaderRows(f, tl)
		}
		frameStart = ri
	}
	for ri := 0; ri < len(tl.rows); {
		end := tl.blockEnd(ri)
		f.skipFloats(f.frame().originX, f.frame().originX+tl.width(), tl.rowsHeight(ri, end))
		room := f.frame().bottomLimit - f.top
		if ri >= tl.headerRows {
			room -= tl.rowsHeight(0, min(tl.headerRows, len(tl.rows)))
		}
		if h := tl.rowsHeight(ri, end); f.y+h > f.frame().bottomLimit && !f.atTop && h <= room {
			nextFrame(ri)
		}
		for ; ri < end; ri++ {
			rh := tl.rows[ri].height
			notes := rowNotes(tl.rows[ri])
			if ri != frameStart && (f.y+rh > f.frame().bottomLimit || !f.notesFit(notes, rh)) && !f.atTop {
				nextFrame(ri)
			}
			drawRow(f.r, tl, ri, f.frame().originX, f.y, &pageCut{bottom: f.frame().bottomLimit, frameStart: ri == frameStart, drawn: drawn, vertical: vertical})
			f.y += rh
			f.atTop = false
			f.addNotes(notes)
		}
	}
}

// rowNotes lists the footnotes the row's cells reference, nested tables
// included.
func rowNotes(row rowLayout) []*Note {
	var notes []*Note
	for _, cl := range row.cells {
		notes = append(notes, contentNotes(cl.content)...)
	}
	return notes
}

func contentNotes(c cellContent) []*Note {
	var notes []*Note
	for _, it := range c.items {
		switch {
		case it.table != nil:
			for _, row := range it.table.rows {
				notes = append(notes, rowNotes(row)...)
			}
		default:
			for _, ln := range it.para.lines {
				notes = append(notes, lineNotes(ln.words)...)
			}
		}
	}
	return notes
}

// floatOrigin places a floating object w × h - a table or a picture: each axis
// is measured from its anchor - the text column (vertically, the cursor), the
// page's margin, or the page's edge - either by the offset the object declares
// or, when it names an alignment instead, by aligning it against that anchor.
func floatOrigin(f *flow, fl Float, w, h float64) (float64, float64) {
	g := f.sec.sec.Geometry
	fr := f.frame()

	x, availW := fr.originX, fr.contentWidth
	switch fl.HorzAnchor {
	case AnchorPage:
		x, availW = 0, g.WidthPt
	case AnchorMargin:
		x, availW = g.MarginLeftPt, g.WidthPt-g.MarginLeftPt-g.MarginRightPt
	}
	switch fl.XSpec {
	case "center":
		x += (availW - w) / 2
	case "right", "outside":
		x += availW - w
	case "left", "inside":
	default:
		x += fl.XPt
	}

	y, availH := f.y, fr.bottomLimit-f.y
	switch fl.VertAnchor {
	case AnchorPage:
		y, availH = 0, g.HeightPt
	case AnchorMargin:
		y, availH = g.MarginTopPt, g.HeightPt-g.MarginTopPt-g.MarginBottomPt
	}
	switch fl.YSpec {
	case "center":
		y += (availH - h) / 2
	case "bottom", "outside":
		y += availH - h
	case "top", "inside":
	default:
		y += fl.YPt
	}
	return x, y
}

// drawHeaderRows repeats the table's header rows at the cursor, advancing it
// past them.
func drawHeaderRows(f *flow, tl tableLayout) {
	for ri := 0; ri < tl.headerRows && ri < len(tl.rows); ri++ {
		drawRow(f.r, tl, ri, f.frame().originX, f.y, nil)
		f.y += tl.rows[ri].height
		f.atTop = false
	}
}

// pageCut is what drawRow needs to fit a row onto a frame the table is
// paginated across: the frame's lower limit, whether the row opens a frame the
// table broke onto, and - per vertical merge, keyed by its restart row and
// column - how far earlier frames already drew the merged cell's content.
type pageCut struct {
	bottom     float64
	frameStart bool
	drawn      map[[2]int]float64
	// vertical is, per vertical-text merge cut across frames, where its text
	// has got to.
	vertical map[[2]int]*verticalCut
}

// verticalCut is how far the text of a vertical cell cut across frames has
// been drawn: the paragraph it is in, and the line breaker positioned in it.
// Its lines run along the row's height, so each frame's part is packed again at
// the length that frame gives it.
type verticalCut struct {
	paras   []Paragraph
	pi      int
	b       lineBreaker
	started bool // b is positioned in paras[pi]
}

// part packs the next lines of the cell's text at the given length, as many as
// fit across room - always at least one, so the text moves on.
func (v *verticalCut) part(r renderer, length, room float64) cellContent {
	var cc cellContent
	var box *paraBox
	for v.pi < len(v.paras) {
		p := v.paras[v.pi]
		if !v.started {
			v.b, v.started, box = newLineBreaker(r, p), true, nil
		}
		if v.b.done() {
			v.pi, v.started = v.pi+1, false
			continue
		}
		retry := v.b
		ln, _ := v.b.next(length - p.Props.Indent.LeftPt - p.Props.Indent.RightPt)
		if cc.height > 0 && cc.height+ln.height > room {
			v.b = retry
			break
		}
		if box == nil {
			box = &paraBox{align: p.Props.Alignment, indent: p.Props.Indent, spacing: p.Props.Spacing, shading: p.Props.Shading, tabs: p.Props.Tabs}
			cc.items = append(cc.items, cellItem{para: box})
		}
		box.lines = append(box.lines, ln)
		cc.height += ln.height
	}
	return cc
}

// drawVertical draws a vertical cell's part on this frame: its text packed
// again for the box's height and picked up where the previous frame stopped.
func (c *pageCut) drawVertical(r renderer, cl cellLayout, key [2]int, x, y, width, height float64) {
	v := c.vertical[key]
	if v == nil {
		v = &verticalCut{}
		for _, el := range cl.cell.blocks() {
			if el.Paragraph != nil {
				v.paras = append(v.paras, *el.Paragraph)
			}
		}
		c.vertical[key] = v
	}
	m := cl.margins
	part := cl
	part.content = v.part(r, height-m.LeftPt-m.RightPt, width-m.TopPt-m.BottomPt)
	drawCellBox(r, part, x, y, width, height, true, wholeContent)
}

// window picks the part of cl's (horizontal) content a box of the given height
// draws on this frame: from where the previous frame stopped, to the last whole
// line that fits when the cell is cut again here (more), else to the end. It
// records where it stopped. Vertical text is cut by drawVertical instead.
func (c *pageCut) window(cl cellLayout, key [2]int, height float64, more bool) contentWindow {
	from := c.drawn[key]
	to := math.Inf(1)
	if more {
		to = cl.content.breakAt(from, from+height-cl.margins.TopPt-cl.margins.BottomPt)
	}
	c.drawn[key] = to
	return contentWindow{from: from, to: to}
}

// drawRow draws every non-continuation cell of row ri, a merged cell across all
// the rows it spans. With a cut, nothing goes below the frame's limit: a merged
// cell reaching past it is cut at its last whole line, and on the frame the
// table continues onto, the first row draws the rest of that cell - its box and
// the lines still to come. vMerge-continue cells are otherwise skipped, since
// the restart cell above them drew their area. A nil cut draws unbounded.
func drawRow(r renderer, tl tableLayout, ri int, x0, y float64, cut *pageCut) {
	row := tl.rows[ri]
	for _, cl := range row.cells {
		x := x0 + tl.colOffsets[cl.col]
		if cl.cell.VMerge == VMergeContinue {
			if cut == nil || !cut.frameStart {
				continue
			}
			start, ok := tl.mergeStart(ri, cl.col)
			if !ok {
				continue
			}
			rc, _ := tl.rows[start].cellAt(cl.col)
			rest := tl.mergedHeight(start, cl.col) - tl.rowsHeight(start, ri)
			height := min(rest, cut.bottom-y)
			width := spanWidth(tl.colWidths, cl.col, colSpan(rc.cell))
			if rc.cell.TextDirection != TextDirectionHorizontal {
				cut.drawVertical(r, rc, [2]int{start, cl.col}, x, y, width, height)
				continue
			}
			win := cut.window(rc, [2]int{start, cl.col}, height, rest > height)
			drawCellBox(r, rc, x, y, width, height, true, win)
			continue
		}
		width := spanWidth(tl.colWidths, cl.col, colSpan(cl.cell))
		height, clip, win := tl.mergedHeight(ri, cl.col), row.fixed, wholeContent
		if cut != nil && y+height > cut.bottom {
			height, clip = cut.bottom-y, true
			if cl.cell.TextDirection != TextDirectionHorizontal {
				cut.drawVertical(r, cl, [2]int{ri, cl.col}, x, y, width, height)
				continue
			}
			win = cut.window(cl, [2]int{ri, cl.col}, height, true)
		}
		drawCellBox(r, cl, x, y, width, height, clip, win)
	}
}

// contentWindow is the slice of a cell's content a box draws, in content
// coordinates (0 is the top of its first line): the units - lines, empty
// paragraphs, nested tables - lying wholly within [from, to].
type contentWindow struct{ from, to float64 }

// wholeContent draws all of a cell's content.
var wholeContent = contentWindow{to: math.Inf(1)}

// unitBottoms lists the content-relative bottom of each unit a cell's content
// can be cut after, in order.
func (c cellContent) unitBottoms() []float64 {
	var out []float64
	var y float64
	for _, it := range c.items {
		switch {
		case it.table != nil:
			y += it.table.totalHeight
			out = append(out, y)
		case len(it.para.lines) == 0:
			y += lineHeightFor(defaultRenderSizePt, 0, it.para.spacing)
			out = append(out, y)
		default:
			for _, ln := range it.para.lines {
				y += ln.height
				out = append(out, y)
			}
		}
	}
	return out
}

// breakAt returns where to cut content that has been drawn up to from so that
// what is drawn next ends by limit: the bottom of the last unit that fits. When
// not even one more unit fits, it takes that unit anyway (to be clipped) so the
// content always moves on.
func (c cellContent) breakAt(from, limit float64) float64 {
	const eps = 1e-6
	cut := from
	for _, b := range c.unitBottoms() {
		if b <= from+eps {
			continue
		}
		if b > limit+eps {
			if cut == from {
				return b
			}
			return cut
		}
		cut = b
	}
	return cut
}

// drawCellBox draws one cell's shading, borders and content within the rectangle
// [x, x+width] x [y, y+height]. A cell with vertical text (w:textDirection) has
// its content drawn into that rectangle turned 90°: the box is rotated about one
// of its corners, so the same horizontal layout produces text reading bottom-to-
// top (btLr) or top-to-bottom (tbRl). In a row of exact height (w:hRule="exact")
// the content is clipped to the cell, as Word clips it, instead of spilling over
// the row below.
// win picks the part of the content drawn here (see contentWindow).
func drawCellBox(r renderer, cl cellLayout, x, y, width, height float64, clip bool, win contentWindow) {
	if cl.cell.Shading != "" {
		r.FillRect(x, y, width, height, cl.cell.Shading)
	}
	drawCellBorders(r, cl.cell.Borders, x, y, width, height)

	if clip {
		r.Clip(x, y, width, height)
		defer r.ClipEnd()
	}
	switch cl.cell.TextDirection {
	case TextDirectionBTLR:
		// Rotating counter-clockwise about the bottom-left corner turns a box that
		// runs right-and-down from there into the cell's own rectangle.
		r.Rotate(90, x, y+height)
		drawCellContent(r, cl, x, y+height, height, width, win)
		r.RotateEnd()
	case TextDirectionTBRL:
		r.Rotate(-90, x+width, y)
		drawCellContent(r, cl, x+width, y, height, width, win)
		r.RotateEnd()
	default:
		drawCellContent(r, cl, x, y, width, height, win)
	}
}

// drawCellContent draws the window win of a cell's paragraphs and nested tables
// inside the box at (x, y) of the given size, inset by the cell's margins. Whole
// content is pushed down by the cell's vertical alignment (w:vAlign) when it is
// shorter than the box; a window of content split across frames starts at the
// top.
func drawCellContent(r renderer, cl cellLayout, x, y, width, height float64, win contentWindow) {
	const eps = 1e-6
	m := cl.margins
	top := y + m.TopPt - win.from // page y of content-relative 0
	if win == wholeContent {
		if free := height - m.TopPt - m.BottomPt - cl.content.height; free > 0 {
			switch cl.cell.VAlign {
			case VAlignCenter:
				top += free / 2
			case VAlignBottom:
				top += free
			case VAlignTop:
			}
		}
	}
	visible := func(from, to float64) bool { return from >= win.from-eps && to <= win.to+eps }
	var pos float64 // content-relative top of the next unit
	for _, it := range cl.content.items {
		if it.table != nil {
			if visible(pos, pos+it.table.totalHeight) {
				drawTableRows(r, *it.table, x+m.LeftPt, top+pos)
			}
			pos += it.table.totalHeight
			continue
		}
		pb := it.para
		if len(pb.lines) == 0 {
			pos += lineHeightFor(defaultRenderSizePt, 0, pb.spacing)
			continue
		}
		innerX := x + m.LeftPt + pb.indent.LeftPt
		innerWidth := width - m.LeftPt - m.RightPt - pb.indent.LeftPt - pb.indent.RightPt
		for i, ln := range pb.lines {
			if visible(pos, pos+ln.height) {
				if pb.shading != "" { // paragraph shading sits over the cell's, under the text
					r.FillRect(innerX, top+pos, innerWidth, ln.height, pb.shading)
				}
				drawBarTabs(r, pb.tabs, x+m.LeftPt, top+pos, ln.height)
				if i == 0 {
					for _, name := range pb.bookmarks {
						r.Anchor(name, top+pos)
					}
				}
				drawLine(r, ln, pb.align, innerX, innerWidth, top+pos, i == len(pb.lines)-1, pb.indent.FirstLineOffsetPt, i == 0)
			}
			pos += ln.height
		}
	}
}

// drawTableRows draws every row of tl starting at (x, y) with no pagination
// of its own; a nested table's total height is already folded into its parent
// cell's row height, so it fits unless taller than a full page (out of scope).
func drawTableRows(r renderer, tl tableLayout, x, y float64) {
	cy := y
	for ri := range tl.rows {
		drawRow(r, tl, ri, x, cy, nil)
		cy += tl.rows[ri].height
	}
}

func drawCellBorders(r renderer, b CellBorders, x, y, w, h float64) {
	drawBorderSide(r, b.Top, x, y, x+w, y)
	drawBorderSide(r, b.Bottom, x, y+h, x+w, y+h)
	drawBorderSide(r, b.Left, x, y, x, y+h)
	drawBorderSide(r, b.Right, x+w, y, x+w, y+h)
	drawBorderSide(r, b.DiagDown, x, y, x+w, y+h)
	drawBorderSide(r, b.DiagUp, x, y+h, x+w, y)
}

func drawBorderSide(r renderer, side BorderSide, x1, y1, x2, y2 float64) {
	if side.Style == "" {
		return
	}
	r.StrokeLine(x1, y1, x2, y2, side.WidthPt, side.Color)
}
