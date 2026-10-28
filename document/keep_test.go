package document

import (
	"math"
	"testing"
)

// keepLineHeight is the height of one line of the 12pt text these tests use.
var keepLineHeight = 12 * lineSpacing

// fillTo returns one-line paragraphs that leave room for exactly room lines at
// the foot of the first page.
func fillTo(room int) []BodyElement {
	perPage := int(math.Floor(contentHeightPt / keepLineHeight))
	body := make([]BodyElement, perPage-room)
	for i := range body {
		p := para(AlignLeft, false, run("filler", 12))
		body[i] = BodyElement{Paragraph: &p}
	}
	return body
}

// pageOf is the page the text was drawn on, or 0 when it was not drawn.
func pageOf(f *fakeRenderer, text string) int {
	for _, d := range f.draws {
		if d.text == text {
			return d.page
		}
	}
	return 0
}

func lineRuns(texts ...string) []Run {
	var runs []Run
	for i, s := range texts {
		if i > 0 {
			runs = append(runs, Run{LineBreak: true})
		}
		runs = append(runs, run(s, 12))
	}
	return runs
}

func TestKeepLines(t *testing.T) {
	tests := []struct {
		name      string
		keepLines bool
		wantFirst int // the page the paragraph's first line lands on
	}{
		{"split without it", false, 1},
		{"moved whole with it", true, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := para(AlignLeft, false, lineRuns("one", "two", "three")...)
			p.Props.KeepLines = tt.keepLines
			body := append(fillTo(2), BodyElement{Paragraph: &p})
			f := &fakeRenderer{}
			(&Converter{doc: &Document{Body: body}}).render(f)

			if got := pageOf(f, "one"); got != tt.wantFirst {
				t.Errorf("KeepLines=%v: first line on page %d, want %d", tt.keepLines, got, tt.wantFirst)
			}
			if got := pageOf(f, "three"); got != 2 {
				t.Errorf("KeepLines=%v: last line on page %d, want 2", tt.keepLines, got)
			}
		})
	}
}

func TestKeepLines_TallerThanAPageStillSplits(t *testing.T) {
	perPage := int(math.Floor(contentHeightPt / keepLineHeight))
	texts := make([]string, perPage+2)
	for i := range texts {
		texts[i] = "line"
	}
	texts[0] = "first"
	p := para(AlignLeft, false, lineRuns(texts...)...)
	p.Props.KeepLines = true
	body := append(fillTo(2), BodyElement{Paragraph: &p})
	f := &fakeRenderer{}
	(&Converter{doc: &Document{Body: body}}).render(f)

	if got := pageOf(f, "first"); got != 1 {
		t.Errorf("first line on page %d, want 1 (a paragraph no page can hold is not moved)", got)
	}
}

func TestKeepNext(t *testing.T) {
	table := Table{ColumnWidths: []float64{100}, Rows: []Row{mkRow(Cell{
		ColSpan:    1,
		Paragraphs: []Paragraph{para(AlignLeft, false, lineRuns("r1", "r2")...)},
	})}}
	next := para(AlignLeft, false, run("next", 12))
	tests := []struct {
		name     string
		keepNext bool
		room     int
		follow   []BodyElement
		want     int // the heading's page
	}{
		{"stays without it", false, 1, []BodyElement{{Paragraph: &next}}, 1},
		{"moves with the next paragraph", true, 1, []BodyElement{{Paragraph: &next}}, 2},
		{"stays when both fit", true, 2, []BodyElement{{Paragraph: &next}}, 1},
		{"moves with the table's first row", true, 2, []BodyElement{{Table: &table}}, 2},
		{"last element has nothing to keep with", true, 1, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			heading := para(AlignLeft, false, run("heading", 12))
			heading.Props.KeepNext = tt.keepNext
			body := append(fillTo(tt.room), BodyElement{Paragraph: &heading})
			body = append(body, tt.follow...)
			f := &fakeRenderer{}
			(&Converter{doc: &Document{Body: body}}).render(f)

			if got := pageOf(f, "heading"); got != tt.want {
				t.Errorf("heading on page %d, want %d", got, tt.want)
			}
		})
	}
}

func TestKeepNext_Chain(t *testing.T) {
	// Two headings kept with each other and the text: all three need room for
	// three lines, and only two are left.
	h1 := para(AlignLeft, false, run("h1", 12))
	h1.Props.KeepNext = true
	h2 := para(AlignLeft, false, run("h2", 12))
	h2.Props.KeepNext = true
	text := para(AlignLeft, false, lineRuns("t1", "t2")...)
	body := append(fillTo(2), BodyElement{Paragraph: &h1}, BodyElement{Paragraph: &h2}, BodyElement{Paragraph: &text})
	f := &fakeRenderer{}
	(&Converter{doc: &Document{Body: body}}).render(f)

	for _, s := range []string{"h1", "h2", "t1"} {
		if got := pageOf(f, s); got != 2 {
			t.Errorf("%q on page %d, want 2", s, got)
		}
	}
}

func TestKeepLines_SpaceAfterMayOverhang(t *testing.T) {
	p := para(AlignLeft, false, lineRuns("one", "two")...)
	p.Props.KeepLines = true
	p.Props.Spacing.AfterPt = 3 * keepLineHeight
	body := append(fillTo(2), BodyElement{Paragraph: &p})
	f := &fakeRenderer{}
	(&Converter{doc: &Document{Body: body}}).render(f)

	if got := pageOf(f, "one"); got != 1 {
		t.Errorf("first line on page %d, want 1 (the lines fit; only the space after does not)", got)
	}
}
