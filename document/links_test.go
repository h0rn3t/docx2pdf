package document

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

// linkRels declares one external hyperlink relationship per id → URL pair.
func linkRels(pairs ...string) string {
	var rels strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		rels.WriteString(`<Relationship Id="` + pairs[i] + `" TargetMode="External"` +
			` Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="` + pairs[i+1] + `"/>`)
	}
	return docxtest.Rels(rels.String())
}

func hyperlink(attrs, text string) string {
	return `<w:hyperlink xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` + attrs +
		`><w:r><w:t>` + text + `</w:t></w:r></w:hyperlink>`
}

func TestOpen_HyperlinkTargets(t *testing.T) {
	tests := []struct {
		name  string
		attrs string
		want  string
	}{
		{name: "external", attrs: `r:id="rId5"`, want: "https://example.com/a"},
		{name: "bookmark", attrs: `w:anchor="intro"`, want: "#intro"},
		{name: "external with fragment", attrs: `r:id="rId5" w:anchor="top"`, want: "https://example.com/a#top"},
		{name: "unsafe scheme dropped", attrs: `r:id="rId6"`, want: ""},
		{name: "unknown id", attrs: `r:id="rId9"`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := openFixture(t, docxtest.BuildWith(t,
				`<w:p><w:r><w:t>see </w:t></w:r>`+hyperlink(tt.attrs, "here")+`</w:p>`,
				map[string]string{"word/_rels/document.xml.rels": linkRels(
					"rId5", "https://example.com/a", "rId6", "javascript:alert(1)")}))
			runs := doc.Body[0].Paragraph.Runs
			if runs[0].Link != "" {
				t.Errorf("run outside the hyperlink has Link %q, want none", runs[0].Link)
			}
			if got := runs[1].Link; got != tt.want {
				t.Errorf("hyperlink %s: Link = %q, want %q", tt.attrs, got, tt.want)
			}
		})
	}
}

func TestOpen_BookmarksBelongToTheirParagraph(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t,
		`<w:p><w:bookmarkStart w:id="0" w:name="intro"/><w:r><w:t>Intro</w:t></w:r><w:bookmarkEnd w:id="0"/></w:p>`))
	if got := doc.Body[0].Paragraph.Bookmarks; !slices.Equal(got, []string{"intro"}) {
		t.Errorf("Bookmarks = %q, want [intro]", got)
	}
}

func TestRender_LinkCoversItsWordsAndBookmarkIsAnchored(t *testing.T) {
	link := RunProperties{SizePt: 12}
	body := bodyOf(
		Paragraph{Runs: []Run{run("go", 12), {Text: " to it", Props: link, Link: "#end"}, run(" now", 12)}},
		Paragraph{Runs: []Run{run("End", 12)}, Bookmarks: []string{"end"}},
	)
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: body}).render(f)

	if len(f.links) != 1 {
		t.Fatalf("links = %+v, want one rectangle", f.links)
	}
	l := f.links[0]
	if from, to := drawnX(t, f, "to"), drawnX(t, f, "it")+12; l.x != from || l.x+l.w != to || l.target != "#end" {
		t.Errorf("link = %+v, want #end from x=%.1f to x=%.1f", l, from, to)
	}
	want := []anchorCall{{page: 1, name: "end", y: testPage.originY + 12*lineSpacing}}
	if !slices.Equal(f.anchors, want) {
		t.Errorf("anchors = %+v, want %+v", f.anchors, want)
	}
}

func TestConvert_HyperlinkBecomesAPDFLinkAnnotation(t *testing.T) {
	doc := openFixture(t, docxtest.BuildWith(t,
		`<w:p>`+hyperlink(`r:id="rId5"`, "site")+hyperlink(`w:anchor="nowhere"`, "lost")+`</w:p>`,
		map[string]string{"word/_rels/document.xml.rels": linkRels("rId5", "https://example.com/")}))
	var buf bytes.Buffer
	if err := ConvertToPdf(doc).Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	pdf := buf.String()
	if !strings.Contains(pdf, "/URI (https://example.com/)") {
		t.Error("PDF has no URI link to https://example.com/")
	}
	// The link to a bookmark that does not exist must still point at a real page.
	if strings.Contains(pdf, "/Dest [1 0 R") {
		t.Error("an unplaced bookmark link points at page 0")
	}
}
