package document

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"

	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

func TestSections_PagesAfterAContinuousBreakStartAtTheTopMargin(t *testing.T) {
	f := &fakeRenderer{}
	first := para(AlignLeft, false, run("a", 12))
	rest := repeatParas(linesPerFrame()+2, "x") // overflows onto a second page
	body := bodyOf(append([]Paragraph{first}, rest...)...)
	doc := &Document{Body: body, Geometry: testGeometry, Sections: []Section{
		{Geometry: testGeometry, Columns: []Column{{WidthPt: contentWidthPt}}, End: 1},
		{Geometry: testGeometry, Columns: []Column{{WidthPt: contentWidthPt}}, End: len(body), Continuous: true},
	}}
	ConvertToPdf(doc).render(f)

	if f.page != 2 {
		t.Fatalf("pages = %d, want 2", f.page)
	}
	for _, d := range f.draws {
		if d.page == 2 {
			if want := f.draws[0].y; d.y != want {
				t.Errorf("first baseline on page 2 = %.2f, want %.2f (the top margin, like page 1)", d.y, want)
			}
			return
		}
	}
	t.Fatal("nothing was drawn on page 2")
}

func TestLayout_WordSplitAcrossRunsWrapsAsOneUnit(t *testing.T) {
	// 6 pt per glyph at 12 pt: "aaaa bb" is 42 pt and fits in 50, "aaaa bbcc" (54) does not.
	p := para(AlignLeft, false, run("aaaa bb", 12), run("cc", 12))
	lines := layoutParagraph(&fakeRenderer{}, p, 50)

	var got []string
	for _, ln := range lines {
		var b strings.Builder
		for _, w := range ln.words {
			b.WriteString(w.text)
		}
		got = append(got, b.String())
	}
	if want := []string{"aaaa", "bbcc"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("layoutParagraph(%q+%q, 50) lines = %q, want %q", "aaaa bb", "cc", got, want)
	}
}

func TestOpen_TabCrAndNoBreakHyphenAreKept(t *testing.T) {
	doc := openFixture(t, docxtest.BuildWith(t,
		`<w:p><w:r><w:t>Name</w:t><w:tab/><w:t>Value</w:t><w:noBreakHyphen/><w:t>x</w:t><w:cr/><w:t>y</w:t></w:r></w:p>`, nil))

	runs := doc.Body[0].Paragraph.Runs
	var b strings.Builder
	for _, r := range runs {
		switch {
		case r.LineBreak:
			b.WriteString("\n")
		case r.Tab:
			b.WriteString("\t")
		}
		b.WriteString(r.Text)
	}
	if got, want := b.String(), "Name\tValue-x\ny"; got != want {
		t.Errorf("paragraph text = %q, want %q", got, want)
	}
}

func TestResolveBorderSide_NilValueRemovesTheBorder(t *testing.T) {
	tbl := &xmlBorder{Val: "single", Sz: "4"}
	if got := resolveBorderSide(tbl, &xmlBorder{Val: "nil"}); got != (BorderSide{}) {
		t.Errorf("resolveBorderSide(single, nil) = %+v, want no border", got)
	}
}

func TestFormatNumber_LettersRepeatPastZ(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "a"}, {26, "z"}, {27, "aa"}, {28, "bb"}, {53, "aaa"},
		{maxLetterNumber + 1, "781"}, // bounded, not 31+ letters
	}
	for _, tt := range tests {
		if got := formatNumber(tt.n, "lowerLetter"); got != tt.want {
			t.Errorf("formatNumber(%d, lowerLetter) = %q, want %q", tt.n, got, tt.want)
		}
	}
	if got := formatNumber(4000, "upperRoman"); got != "4000" {
		t.Errorf("formatNumber(4000, upperRoman) = %q, want decimal", got)
	}
}

func TestUntrustedCountsAreClamped(t *testing.T) {
	if got := gridSpan(&xmlTcPr{GridSpan: &xmlVal{Val: "2000000000"}}); got != maxGridColumns {
		t.Errorf("gridSpan(2e9) = %d, want %d", got, maxGridColumns)
	}
	if got := len(buildColumns(&xmlCols{Num: "2000000000", Space: "0"}, 450)); got != maxColumns {
		t.Errorf("len(buildColumns(num=2e9)) = %d, want %d", got, maxColumns)
	}
}

func TestLineSpacingFrom_ZeroLineIsSingle(t *testing.T) {
	if rule, v := lineSpacingFrom("0", "auto"); rule != LineSpacingSingle {
		t.Errorf("lineSpacingFrom(0, auto) = %v, %v, want single", rule, v)
	}
}

func TestBuildPageGeometry_NegativeTopMarginIsItsAbsoluteValue(t *testing.T) {
	g := buildPageGeometry(&xmlSectPr{PgMar: &xmlPgMar{Top: "-1440", Bottom: "-720"}})
	if g.MarginTopPt != 72 || g.MarginBottomPt != 36 {
		t.Errorf("margins top/bottom = %v/%v, want 72/36", g.MarginTopPt, g.MarginBottomPt)
	}
}

func TestOpen_OversizedPartIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bomb.docx")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(w, zeroReader{}, maxPartSize+1); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	doc, err := Open(path)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Open(oversized) = %v, %v; want a size error", doc, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// drawingDoc is a document whose single paragraph draws the media part target.
func drawingDoc(t *testing.T, target, data string) *Document {
	t.Helper()
	body := `<w:p><w:r><w:drawing>
		<wp:inline xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing">
			<wp:extent cx="914400" cy="914400"/>
			<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData>
				<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:blipFill>
					<a:blip xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:embed="rId5"/>
				</pic:blipFill></pic:pic>
			</a:graphicData></a:graphic>
		</wp:inline>
	</w:drawing></w:r></w:p>`
	return openFixture(t, docxtest.BuildWith(t, body, map[string]string{
		"word/_rels/document.xml.rels": docxtest.Rels(docxtest.Rel("rId5", "image", target)),
		"word/" + target:               data,
	}))
}

func TestImages_ThatTheBackendWouldRejectDoNotFailThePDF(t *testing.T) {
	tests := []struct {
		name, target, data string
		wantType           string // "" means the picture is dropped
	}{
		{"interlaced png", "media/image1.png", interlacedPNG(t), "PNG"},
		{"png named .jpg", "media/image1.jpg", docxtest.PNG(t, 2, 2), "PNG"},
		{"corrupt png", "media/image1.png", "not an image", ""},
		{"tiff", "media/image1.tiff", encoded(t, func(w io.Writer, m image.Image) error { return tiff.Encode(w, m, nil) }), "PNG"},
		{"bmp", "media/image1.bmp", encoded(t, bmp.Encode), "PNG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := drawingDoc(t, tt.target, tt.data)
			var gotType string
			if runs := doc.Body[0].Paragraph.Runs; len(runs) > 0 && runs[0].Image != nil {
				gotType = runs[0].Image.Type
			}
			if gotType != tt.wantType {
				t.Errorf("image type = %q, want %q", gotType, tt.wantType)
			}
			if err := doc.WritePDFTo(io.Discard); err != nil {
				t.Errorf("WritePDFTo() error = %v, want nil", err)
			}
		})
	}
}

// encoded is a 2×2 picture written by encode.
func encoded(t *testing.T, encode func(io.Writer, image.Image) error) string {
	t.Helper()
	var buf bytes.Buffer
	if err := encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// interlacedPNG is a 1×1 PNG flagged Adam7-interlaced. A single pixel's
// scanline is the same either way, so only the IHDR flag (and its CRC) changes.
func interlacedPNG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	b[28] = 1
	binary.BigEndian.PutUint32(b[29:33], crc32.ChecksumIEEE(b[12:29]))
	return string(b)
}

func openFixture(t *testing.T, path string) *Document {
	t.Helper()
	doc, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", path, err)
	}
	return doc
}

// pagesOf maps each drawn text to the page it was drawn on.
func pagesOf(f *fakeRenderer) map[string]int {
	out := make(map[string]int)
	for _, d := range f.draws {
		out[d.text] = d.page
	}
	return out
}

func TestPageBreak_AfterContentSplitsTheParagraph(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t,
		`<w:p><w:r><w:t>one</w:t><w:br w:type="page"/><w:t>two</w:t></w:r></w:p>`+
			`<w:p><w:r><w:t>three</w:t><w:br w:type="page"/></w:r></w:p>`+
			`<w:p><w:r><w:t>four</w:t></w:r></w:p>`))
	if doc.Body[0].Paragraph.Props.PageBreak {
		t.Error("a break after content must not move the whole paragraph")
	}
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	got := pagesOf(f)
	want := map[string]int{"one": 1, "two": 2, "three": 2, "four": 3}
	for text, page := range want {
		if got[text] != page {
			t.Errorf("%q drawn on page %d, want %d", text, got[text], page)
		}
	}
}

func TestPageField_ShowsThePageItIsDrawnOn(t *testing.T) {
	var runs []Run
	for range linesPerFrame() + 1 { // one more line than the page holds
		runs = append(runs, run("x", 12), Run{LineBreak: true})
	}
	runs = append(runs, Run{Field: FieldPage, Text: "?", Props: RunProperties{SizePt: 12}})
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: bodyOf(Paragraph{Runs: runs})}).render(f)

	last := f.draws[len(f.draws)-1]
	if last.page != 2 || last.text != "2" {
		t.Errorf("PAGE field = %q on page %d, want %q on page 2", last.text, last.page, "2")
	}
}

func TestImages_BackendParseFailureSkipsThePicture(t *testing.T) {
	// A header DecodeConfig accepts over an IDAT fpdf cannot inflate.
	b := []byte(docxtest.PNG(t, 8, 8))
	i := bytes.Index(b, []byte("IDAT"))
	n := int(binary.BigEndian.Uint32(b[i-4 : i]))
	for k := range n {
		b[i+4+k] = 0xFF
	}
	doc := drawingDoc(t, "media/image1.png", string(b))
	if err := doc.WritePDFTo(io.Discard); err != nil {
		t.Errorf("WritePDFTo() error = %v, want nil (the picture skipped)", err)
	}
}

func TestOpen_PartsAreFoundThroughRelationships(t *testing.T) {
	ns := `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	path := docxtest.BuildParts(t, map[string]string{
		"_rels/.rels": docxtest.Rels(docxtest.Rel("rId1", "officeDocument", "doc/main.xml")),
		"doc/main.xml": `<w:document ` + ns + `><w:body><w:p><w:pPr><w:pStyle w:val="Big"/></w:pPr>` +
			`<w:r><w:t>hi</w:t></w:r></w:p></w:body></w:document>`,
		"doc/_rels/main.xml.rels": docxtest.Rels(docxtest.Rel("rId1", "styles", "/doc/st.xml")),
		"doc/st.xml": docxtest.Styles(`<w:style w:type="paragraph" w:styleId="Big">` +
			`<w:rPr><w:sz w:val="48"/></w:rPr></w:style>`),
	})
	doc := openFixture(t, path)
	if got := doc.Body[0].Paragraph.Runs[0].Props.SizePt; got != 24 {
		t.Errorf("run size = %v, want 24 (style from the related, root-absolute styles part)", got)
	}
}

func TestHeaders_EvenPagesAndInheritance(t *testing.T) {
	refs := `<w:headerReference w:type="default" r:id="rId10"/><w:headerReference w:type="even" r:id="rId11"/>`
	body := `<w:p><w:pPr>` + sectPrWith(refs) + `</w:pPr><w:r><w:t>s1</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>s2</w:t><w:br w:type="page"/><w:t>s2b</w:t></w:r></w:p>` + sectPrWith("")
	doc := openFixture(t, docxtest.BuildWith(t, body, map[string]string{
		"word/_rels/document.xml.rels": docxtest.Rels(
			docxtest.Rel("rId10", "header", "header1.xml") + docxtest.Rel("rId11", "header", "header2.xml") +
				docxtest.Rel("rId12", "settings", "settings.xml")),
		"word/header1.xml": docxtest.Header(`<w:p><w:r><w:t>odd</w:t></w:r></w:p>`),
		"word/header2.xml": docxtest.Header(`<w:p><w:r><w:t>even</w:t></w:r></w:p>`),
		"word/settings.xml": `<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			`<w:evenAndOddHeaders/></w:settings>`,
	}))
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	headers := map[int]string{}
	for _, d := range f.draws {
		if d.text == "odd" || d.text == "even" {
			headers[d.page] = d.text
		}
	}
	// Page 1: section 1. Pages 2 and 3: section 2, which references no header
	// and so inherits section 1's.
	want := map[int]string{1: "odd", 2: "even", 3: "odd"}
	for page, text := range want {
		if headers[page] != text {
			t.Errorf("header on page %d = %q, want %q", page, headers[page], text)
		}
	}
}

func sectPrWith(inner string) string {
	return `<w:sectPr xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` + inner + `</w:sectPr>`
}

func TestCell_ContentKeepsDocumentOrder(t *testing.T) {
	inner := func(s string) string {
		return `<w:tbl><w:tr><w:tc><w:p><w:r><w:t>` + s + `</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	}
	p := func(s string) string { return `<w:p><w:r><w:t>` + s + `</w:t></w:r></w:p>` }
	doc := openFixture(t, docxtest.Build(t,
		`<w:tbl><w:tr><w:tc>`+p("before")+inner("inner1")+p("after")+inner("inner2")+`</w:tc></w:tr></w:tbl>`))
	f := &fakeRenderer{}
	ConvertToPdf(doc).render(f)

	var order []string
	for _, d := range f.draws {
		order = append(order, d.text)
	}
	if got, want := strings.Join(order, ","), "before,inner1,after,inner2"; got != want {
		t.Errorf("cell draw order = %s, want %s", got, want)
	}
	for i := 1; i < len(f.draws); i++ {
		if f.draws[i].y <= f.draws[i-1].y {
			t.Errorf("%q at y=%.1f is not below %q at y=%.1f", f.draws[i].text, f.draws[i].y, f.draws[i-1].text, f.draws[i-1].y)
		}
	}
}

func TestOpen_WrappedContentIsNotDropped(t *testing.T) {
	doc := openFixture(t, docxtest.Build(t,
		`<w:sdt><w:sdtPr/><w:sdtContent><w:p><w:r><w:t>toc</w:t></w:r></w:p></w:sdtContent></w:sdt>`+
			`<w:p><w:r><w:t>a</w:t></w:r><w:ins><w:r><w:t>b</w:t></w:r></w:ins>`+
			`<w:del><w:r><w:delText>gone</w:delText></w:r></w:del>`+
			`<w:smartTag><w:r><w:t>c</w:t></w:r></w:smartTag></w:p>`))

	var texts []string
	for _, el := range doc.Body {
		var b strings.Builder
		for _, r := range el.Paragraph.Runs {
			b.WriteString(r.Text)
		}
		texts = append(texts, b.String())
	}
	if got, want := strings.Join(texts, "|"), "toc|abc"; got != want {
		t.Errorf("paragraph texts = %q, want %q", got, want)
	}
}

func TestTable_VerticalMergeMovesToTheNextPageTogether(t *testing.T) {
	merged := mkCell(1, VMergeRestart, para(AlignLeft, false, run("merged", 12)))
	tbl := Table{ColumnWidths: []float64{100, 100}, Rows: []Row{
		mkRow(merged, mkCell(1, VMergeNone, para(AlignLeft, false, run("r1", 12)))),
		mkRow(mkCell(1, VMergeContinue), mkCell(1, VMergeNone, para(AlignLeft, false, run("r2", 12)))),
		mkRow(mkCell(1, VMergeContinue), mkCell(1, VMergeNone, para(AlignLeft, false, run("r3", 12)))),
	}}
	// Leave room for one row but not three.
	body := bodyOf(repeatParas(linesPerFrame()-1, "x")...)
	body = append(body, BodyElement{Table: &tbl})
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: body}).render(f)

	got := pagesOf(f)
	for _, text := range []string{"merged", "r1", "r2", "r3"} {
		if got[text] != 2 {
			t.Errorf("%q drawn on page %d, want 2 (the merged rows move on together)", text, got[text])
		}
	}
}

func TestTable_MergeTallerThanAPageIsCutAndContinued(t *testing.T) {
	var lines []Paragraph
	for i := range linesPerFrame() + 10 {
		lines = append(lines, para(AlignLeft, false, run(strconv.Itoa(i), 12)))
	}
	merged := Cell{Paragraphs: lines, VMerge: VMergeRestart, Shading: "#FF0000"}
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{
		mkRow(merged),
		mkRow(Cell{VMerge: VMergeContinue}),
	}}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: []BodyElement{{Table: &tbl}}}).render(f)

	pages := map[int]bool{}
	for _, fl := range f.fills {
		if fl.color != "#FF0000" {
			continue
		}
		pages[fl.page] = true
		if bottom := fl.y + fl.h; bottom > testPage.bottomLimit+1e-9 {
			t.Errorf("merged cell fill on page %d reaches y=%.1f, past the bottom margin %.1f", fl.page, bottom, testPage.bottomLimit)
		}
	}
	if !pages[1] || !pages[2] {
		t.Errorf("merged cell painted on pages %v, want 1 and 2", pages)
	}

	// Every line of the merged cell is drawn once, whole, inside its page.
	seen := map[string]int{}
	for _, d := range f.draws {
		seen[d.text]++
		if top := d.y - 12; top < testPage.originY || d.y > testPage.bottomLimit {
			t.Errorf("line %q on page %d at baseline %.1f is outside the frame", d.text, d.page, d.y)
		}
	}
	for i := range lines {
		if n := seen[strconv.Itoa(i)]; n != 1 {
			t.Errorf("line %d drawn %d times, want once", i, n)
		}
	}
	if last := f.draws[len(f.draws)-1]; last.page != 2 {
		t.Errorf("last line drawn on page %d, want 2 (continued after the cut)", last.page)
	}
}

// drawnX returns where text was drawn, failing the test if it was not.
func drawnX(t *testing.T, f *fakeRenderer, text string) float64 {
	t.Helper()
	for _, d := range f.draws {
		if d.text == text {
			return d.x
		}
	}
	t.Fatalf("%q was not drawn; draws = %+v", text, f.draws)
	return 0
}

func TestTabs_AdvanceToTheirStops(t *testing.T) {
	tab := Run{Tab: true, Props: RunProperties{SizePt: 12}}
	tests := []struct {
		name  string
		props ParagraphProperties
		runs  []Run
		text  string  // the word whose x is checked
		wantX float64 // from the paragraph's left edge
	}{
		{
			name: "default grid",
			runs: []Run{run("ab", 12), tab, run("c", 12)}, // "ab" is 12 pt: the next half-inch stop is 36
			text: "c", wantX: 36,
		},
		{
			name:  "left stop",
			props: ParagraphProperties{Tabs: []TabStop{{PosPt: 100}}},
			runs:  []Run{run("ab", 12), tab, run("c", 12)},
			text:  "c", wantX: 100,
		},
		{
			name:  "right stop ends the text there",
			props: ParagraphProperties{Tabs: []TabStop{{PosPt: 200, Align: TabRight}}},
			runs:  []Run{run("Title", 12), tab, run("12", 12)}, // "12" is 12 pt wide
			text:  "12", wantX: 188,
		},
		{
			name:  "centered stop",
			props: ParagraphProperties{Tabs: []TabStop{{PosPt: 200, Align: TabCenter}}},
			runs:  []Run{tab, run("abcd", 12)}, // 24 pt wide
			text:  "abcd", wantX: 188,
		},
		{
			name:  "stops are measured from the column, not the indent",
			props: ParagraphProperties{Indent: Indent{LeftPt: 50}, Tabs: []TabStop{{PosPt: 100}}},
			runs:  []Run{tab, run("c", 12)},
			text:  "c", wantX: 50,
		},
		{
			name:  "document's own default grid",
			props: ParagraphProperties{DefaultTabStopPt: 50},
			runs:  []Run{run("ab", 12), tab, run("c", 12), tab, run("d", 12)},
			text:  "d", wantX: 100,
		},
		{
			name:  "decimal stop aligns the separator",
			props: ParagraphProperties{Tabs: []TabStop{{PosPt: 200, Align: TabDecimal}}},
			runs:  []Run{tab, run("12,50", 12)}, // "12" is 12 pt wide
			text:  "12,50", wantX: 188,
		},
		{
			name:  "decimal stop without a separator ends the text there",
			props: ParagraphProperties{Tabs: []TabStop{{PosPt: 200, Align: TabDecimal}}},
			runs:  []Run{tab, run("125", 12)},
			text:  "125", wantX: 182,
		},
		{
			name:  "bar stop is skipped",
			props: ParagraphProperties{Tabs: []TabStop{{PosPt: 40, Align: TabBar}, {PosPt: 100}}},
			runs:  []Run{run("ab", 12), tab, run("c", 12)},
			text:  "c", wantX: 100,
		},
		{
			name:  "hanging indent is a stop",
			props: ParagraphProperties{Indent: Indent{LeftPt: 30, FirstLineOffsetPt: -30}},
			runs:  []Run{run("1.", 12), tab, run("c", 12)},
			text:  "c", wantX: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRenderer{}
			tt.props.Alignment = AlignLeft
			ConvertToPdf(&Document{Body: bodyOf(Paragraph{Runs: tt.runs, Props: tt.props})}).render(f)
			left := testPage.originX + tt.props.Indent.LeftPt
			if got := drawnX(t, f, tt.text) - left; got != tt.wantX {
				t.Errorf("%q drawn at %.2f from the paragraph's left edge, want %.2f", tt.text, got, tt.wantX)
			}
		})
	}
}

func TestTabs_DotLeaderFillsTheSpan(t *testing.T) {
	tab := Run{Tab: true, Props: RunProperties{SizePt: 12}}
	p := Paragraph{
		Runs:  []Run{run("Intro", 12), tab, run("7", 12)},
		Props: ParagraphProperties{Tabs: []TabStop{{PosPt: 200, Align: TabRight, Leader: "."}}},
	}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: bodyOf(p)}).render(f)

	var leader string
	for _, d := range f.draws {
		if strings.Trim(d.text, ".") == "" {
			leader = d.text
		}
	}
	// The span runs from "Intro" (30 pt) to "7" (at 194): 164 pt of 6 pt dots, one
	// left off so the leader stops short of the text on either side.
	if want := strings.Repeat(".", 26); leader != want {
		t.Errorf("leader = %q (%d dots), want %d dots", leader, len(leader), len(want))
	}
}

func TestTabs_BarStopDrawsARuleDownEachLine(t *testing.T) {
	p := Paragraph{
		Runs:  []Run{run("a", 12), {LineBreak: true}, run("b", 12)},
		Props: ParagraphProperties{Tabs: []TabStop{{PosPt: 40, Align: TabBar}}},
	}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: bodyOf(p)}).render(f)

	x := testPage.originX + 40
	if len(f.strokes) != 2 {
		t.Fatalf("strokes = %+v, want one rule per line", f.strokes)
	}
	for _, s := range f.strokes {
		if s.x1 != x || s.x2 != x || math.Abs(s.y2-s.y1-12*lineSpacing) > 1e-9 {
			t.Errorf("rule = %+v, want a line-high vertical at x=%.1f", s, x)
		}
	}
}

func TestJustify_StretchesOnlyAfterTheLastTab(t *testing.T) {
	tab := Run{Tab: true, Props: RunProperties{SizePt: 12}}
	text := strings.TrimSpace(strings.Repeat("ab ", 40))
	p := Paragraph{
		Runs:  []Run{run("x y", 12), tab, run(text, 12)},
		Props: ParagraphProperties{Alignment: AlignJustify},
	}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: bodyOf(p)}).render(f)

	// "x" and "y" sit before the tab, so the space between them stays 6 pt.
	if got := drawnX(t, f, "y") - drawnX(t, f, "x"); got != 12 {
		t.Errorf("y - x = %.2f, want 12 (no stretch before the tab)", got)
	}
}

func TestOpen_DefaultTabStopAndBarTab(t *testing.T) {
	doc := openFixture(t, docxtest.BuildWith(t,
		`<w:p><w:pPr><w:tabs><w:tab w:val="bar" w:pos="720"/></w:tabs></w:pPr><w:r><w:t>a</w:t></w:r></w:p>`,
		map[string]string{"word/settings.xml": `<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			`<w:defaultTabStop w:val="1134"/></w:settings>`}))
	props := doc.Body[0].Paragraph.Props
	if props.DefaultTabStopPt != 56.7 {
		t.Errorf("DefaultTabStopPt = %v, want 56.7", props.DefaultTabStopPt)
	}
	if want := []TabStop{{PosPt: 36, Align: TabBar}}; !slices.Equal(props.Tabs, want) {
		t.Errorf("tabs = %+v, want %+v", props.Tabs, want)
	}
}

func TestTabStops_InheritAndClear(t *testing.T) {
	style := &xmlTabs{Tabs: []xmlTab{{Val: "left", Pos: "1440"}, {Val: "right", Pos: "4320", Leader: "dot"}}}
	own := &xmlTabs{Tabs: []xmlTab{{Val: "clear", Pos: "1440"}, {Val: "center", Pos: "2880"}}}
	got := tabStopsFrom(mergeTabs(own, style))
	want := []TabStop{{PosPt: 144, Align: TabCenter}, {PosPt: 216, Align: TabRight, Leader: "."}}
	if !slices.Equal(got, want) {
		t.Errorf("tab stops = %+v, want %+v", got, want)
	}
}

func TestOpen_TabStopsComeFromTheParagraphStyle(t *testing.T) {
	doc := openFixture(t, docxtest.BuildWithStyles(t,
		`<w:p><w:pPr><w:pStyle w:val="TOC1"/></w:pPr><w:r><w:t>a</w:t><w:tab/><w:t>1</w:t></w:r></w:p>`,
		`<w:style w:type="paragraph" w:styleId="TOC1"><w:pPr><w:tabs>`+
			`<w:tab w:val="right" w:leader="dot" w:pos="9350"/></w:tabs></w:pPr></w:style>`))
	want := []TabStop{{PosPt: 467.5, Align: TabRight, Leader: "."}}
	if got := doc.Body[0].Paragraph.Props.Tabs; !slices.Equal(got, want) {
		t.Errorf("tab stops = %+v, want %+v", got, want)
	}
}

func TestSetPageNumber_RemeasuresTheField(t *testing.T) {
	props := RunProperties{SizePt: 12}
	ln := line{words: []word{
		{text: "9", width: 6, props: props, field: FieldPage},
		{text: "x", width: 6, gap: 6, props: props},
	}, natural: 18}
	setPageNumber(&fakeRenderer{}, &ln, 10)

	if got := ln.words[0]; got.text != "10" || got.width != 12 {
		t.Errorf("field word = %q (%.0f pt), want %q (12 pt)", got.text, got.width, "10")
	}
	if ln.natural != 24 {
		t.Errorf("line natural width = %.0f, want 24 (the wider number pushes the rest along)", ln.natural)
	}
}
