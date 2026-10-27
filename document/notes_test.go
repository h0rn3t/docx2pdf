package document

import (
	"math"
	"strings"
	"testing"

	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

// notesPart is a footnotes or endnotes part (root "footnotes"/"endnotes",
// child "footnote"/"endnote") with Word's separator note plus one note per
// id → text pair, each starting with its own number mark.
func notesPart(root, child string, pairs ...string) string {
	var notes strings.Builder
	notes.WriteString(`<w:` + child + ` w:type="separator" w:id="-1"><w:p><w:r><w:separator/></w:r></w:p></w:` + child + `>`)
	for i := 0; i+1 < len(pairs); i += 2 {
		notes.WriteString(`<w:` + child + ` w:id="` + pairs[i] + `"><w:p><w:r><w:` + child + `Ref/></w:r>` +
			`<w:r><w:t xml:space="preserve"> ` + pairs[i+1] + `</w:t></w:r></w:p></w:` + child + `>`)
	}
	return `<w:` + root + ` xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` + notes.String() + `</w:` + root + `>`
}

func openWithNotes(t *testing.T, body string) *Document {
	t.Helper()
	return openFixture(t, docxtest.BuildWith(t, body, map[string]string{
		"word/_rels/document.xml.rels": docxtest.Rels(docxtest.Rel("rId7", "footnotes", "footnotes.xml") +
			docxtest.Rel("rId8", "endnotes", "endnotes.xml")),
		"word/footnotes.xml": notesPart("footnotes", "footnote", "5", "Fifth", "2", "Second"),
		"word/endnotes.xml":  notesPart("endnotes", "endnote", "1", "Closing"),
	}))
}

func TestOpen_NotesAreNumberedInReferenceOrder(t *testing.T) {
	doc := openWithNotes(t, `<w:p><w:r><w:t>a</w:t></w:r><w:r><w:footnoteReference w:id="5"/></w:r>`+
		`<w:r><w:t>b</w:t></w:r><w:r><w:footnoteReference w:id="2"/></w:r>`+
		`<w:r><w:endnoteReference w:id="1"/></w:r><w:r><w:footnoteReference w:id="404"/></w:r></w:p>`)

	runs := doc.Body[0].Paragraph.Runs
	if len(runs) != 5 {
		t.Fatalf("runs = %+v, want a, 1, b, 2, i (the unknown note dropped)", runs)
	}
	for i, want := range map[int]string{1: "1 Fifth", 3: "2 Second", 4: "i Closing"} {
		r := runs[i]
		if r.Note == nil {
			t.Fatalf("run %d (%q) has no note", i, r.Text)
		}
		if got := r.Text + " " + strings.TrimSpace(paragraphText(*r.Note.Body[0].Paragraph)[len(r.Text):]); got != want {
			t.Errorf("run %d: mark and note = %q, want %q", i, got, want)
		}
		if r.Props.BaselineShiftPt <= 0 {
			t.Errorf("run %d: mark %q is not raised", i, r.Text)
		}
	}
	if !runs[4].Note.Endnote || len(doc.Endnotes) != 1 {
		t.Errorf("endnote: Endnote = %v, Document.Endnotes = %d, want true and 1", runs[4].Note.Endnote, len(doc.Endnotes))
	}
}

func paragraphText(p Paragraph) string {
	var b strings.Builder
	for _, r := range p.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

func TestOpen_SuperscriptAndPosition(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t, `<w:p>`+
		`<w:r><w:rPr><w:sz w:val="24"/><w:vertAlign w:val="superscript"/></w:rPr><w:t>2</w:t></w:r>`+
		`<w:r><w:rPr><w:sz w:val="21"/><w:vertAlign w:val="subscript"/></w:rPr><w:t>x</w:t></w:r>`+
		`<w:r><w:rPr><w:position w:val="-6"/></w:rPr><w:t>low</w:t></w:r></w:p>`))
	runs := doc.Body[0].Paragraph.Runs
	want := []struct{ size, shift float64 }{{12 * superSubScale, 4}, {10.5 * superSubScale, -1.5}, {11, -3}}
	for i, w := range want {
		if p := runs[i].Props; math.Abs(p.SizePt-w.size) > 1e-9 || math.Abs(p.BaselineShiftPt-w.shift) > 1e-9 {
			t.Errorf("run %q: size %.3f shift %.2f, want %.3f and %.2f", runs[i].Text, p.SizePt, p.BaselineShiftPt, w.size, w.shift)
		}
	}
}

func footnoteRun(mark, text string) Run {
	return Run{Text: mark, Props: RunProperties{SizePt: 12}, Note: &Note{Body: bodyOf(para(AlignLeft, false, run(text, 12)))}}
}

func TestRender_FootnoteIsDrawnAtTheFootOfItsPage(t *testing.T) {
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: bodyOf(para(AlignLeft, false, run("see", 12), footnoteRun("1", "NOTE")))}).render(f)

	note := drawsOf(f, "NOTE")
	if len(note) != 1 || note[0].page != 1 {
		t.Fatalf("note draws = %+v, want one on page 1", note)
	}
	lineH := 12 * lineSpacing
	if want := testPage.bottomLimit - lineH + 12; note[0].y != want {
		t.Errorf("note baseline = %.2f, want %.2f (its foot at the bottom margin)", note[0].y, want)
	}
	if len(f.strokes) != 1 || f.strokes[0].y1 >= note[0].y-12 {
		t.Errorf("strokes = %+v, want one separator rule above the note", f.strokes)
	}
}

func TestRender_FootnoteReservesItsRoom(t *testing.T) {
	// A frame's worth of lines, the first of which references a two-line note:
	// the last lines no longer fit above the note and move on.
	paras := repeatParas(linesPerFrame(), "x")
	note := Run{Text: "1", Props: RunProperties{SizePt: 12}, Note: &Note{Body: lines(2, "NOTE")}}
	paras[0].Runs = append(paras[0].Runs, note)
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: bodyOf(paras...)}).render(f)

	noteTop := drawsOf(f, "NOTE")[0].y - 12
	for _, d := range drawsOf(f, "x") {
		if d.page == 1 && d.y > noteTop-noteSeparatorPt {
			t.Fatalf("body line at %.2f runs into the notes starting at %.2f", d.y, noteTop)
		}
	}
	if f.page != 2 {
		t.Errorf("pages = %d, want 2", f.page)
	}
}

func TestRender_EndnotesFollowTheBody(t *testing.T) {
	doc := &Document{
		Body:     lines(1, "body"),
		Endnotes: lines(1, "END"),
	}
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	if end, body := drawsOf(f, "END"), drawsOf(f, "body"); len(end) != 1 || end[0].y <= body[0].y {
		t.Errorf("endnote draws = %+v, want one below the body", end)
	}
}
