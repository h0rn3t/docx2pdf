package document

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

// anchoredDrawing is a paragraph holding one wp:anchor picture (rId5, 1" × ½")
// whose anchor element carries attrs and children, with blip carrying the
// a:blip children and srcRect the a:srcRect attributes ("" for none).
func anchoredDrawing(attrs, children, blip, srcRect string) string {
	crop := ""
	if srcRect != "" {
		crop = `<a:srcRect ` + srcRect + `/>`
	}
	return `<w:p><w:r><w:drawing>
		<wp:anchor xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" ` + attrs + `>` + children + `
			<wp:extent cx="914400" cy="457200"/>
			<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData>
				<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:blipFill>
					<a:blip xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:embed="rId5">` + blip + `</a:blip>` + crop + `
				</pic:blipFill></pic:pic>
			</a:graphicData></a:graphic>
		</wp:anchor>
	</w:drawing></w:r></w:p>`
}

func openAnchored(t *testing.T, body string) *Document {
	t.Helper()
	return openFixture(t, docxtest.BuildWith(t, body, map[string]string{
		"word/_rels/document.xml.rels": docxtest.Rels(docxtest.Rel("rId5", "image", "media/image1.png")),
		"word/media/image1.png":        redPNG(t),
	}))
}

// redPNG is a 2×2 opaque red picture.
func redPNG(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := range 4 {
		img.Set(i%2, i/2, color.NRGBA{R: 255, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestOpen_AnchoredPictureIsFloating(t *testing.T) {
	tests := []struct {
		name     string
		attrs    string
		children string
		want     Float
	}{
		{
			name:  "offsets and square wrap",
			attrs: `distL="114300" distR="127000"`,
			children: `<wp:positionH relativeFrom="page"><wp:posOffset>1270000</wp:posOffset></wp:positionH>` +
				`<wp:positionV relativeFrom="paragraph"><wp:posOffset>254000</wp:posOffset></wp:positionV><wp:wrapSquare wrapText="bothSides"/>`,
			want: Float{XPt: 100, YPt: 20, HorzAnchor: AnchorPage, VertAnchor: AnchorText, DistLeftPt: 9, DistRightPt: 10},
		},
		{
			name: "alignment and top-and-bottom wrap",
			children: `<wp:positionH relativeFrom="margin"><wp:align>center</wp:align></wp:positionH>` +
				`<wp:positionV relativeFrom="page"><wp:align>bottom</wp:align></wp:positionV><wp:wrapTopAndBottom/>`,
			want: Float{XSpec: "center", YSpec: "bottom", HorzAnchor: AnchorMargin, VertAnchor: AnchorPage, Wrap: WrapTopAndBottom},
		},
		{
			name:     "simple position, no wrap",
			attrs:    `simplePos="1"`,
			children: `<wp:simplePos x="635000" y="1270000"/><wp:wrapNone/>`,
			want:     Float{XPt: 50, YPt: 100, HorzAnchor: AnchorPage, VertAnchor: AnchorPage, Wrap: WrapNone},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := openAnchored(t, anchoredDrawing(tt.attrs, tt.children, "", ""))
			img := doc.Body[0].Paragraph.Runs[0].Image
			if img == nil || img.Float == nil {
				t.Fatalf("anchored picture: Image = %+v, want a floating one", img)
			}
			if *img.Float != tt.want {
				t.Errorf("Float = %+v, want %+v", *img.Float, tt.want)
			}
		})
	}
}

func TestOpen_PictureCropAndEffects(t *testing.T) {
	doc := openAnchored(t, anchoredDrawing("", "", `<a:grayscl/>`, `l="25000" t="10000" r="0" b="-5000"`))
	img := doc.Body[0].Paragraph.Runs[0].Image
	if want := (Crop{Left: 0.25, Top: 0.1, Bottom: -0.05}); img.Crop != want {
		t.Errorf("Crop = %+v, want %+v", img.Crop, want)
	}
	if img.Name == "word/media/image1.png" {
		t.Errorf("recolored picture keeps the original's name %q; it would share its cache entry", img.Name)
	}
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("decode recolored picture: %v", err)
	}
	if r, g, b, _ := decoded.At(0, 0).RGBA(); r != g || g != b {
		t.Errorf("grayscale pixel = (%d, %d, %d), want equal channels", r, g, b)
	}
}

func TestRender_CroppedPictureIsClippedToItsVisiblePart(t *testing.T) {
	img := &Image{Name: "p", WidthPt: 50, HeightPt: 40, Crop: Crop{Left: 0.5}}
	f := &fakeRenderer{}
	drawPicture(f, img, 10, 20)

	if len(f.clips) != 1 || f.clips[0] != (clipCall{x: 10, y: 20, w: 50, h: 40}) {
		t.Errorf("clips = %+v, want the 50 × 40 box at (10, 20)", f.clips)
	}
	// The visible right half is 50 pt wide, so the whole picture is 100 pt,
	// its left half hanging out to the left of the box.
	if want := (imageCall{x: -40, y: 20, w: 100, h: 40, name: "p"}); len(f.images) != 1 || f.images[0] != want {
		t.Errorf("images = %+v, want %+v", f.images, want)
	}
}

// floatingPicture is a run with a w × h picture floating at (x, y) on the page.
func floatingPicture(x, y, w, h float64, wrap Wrap) Run {
	return Run{Image: &Image{Name: "f", WidthPt: w, HeightPt: h, Float: &Float{
		XPt: x, YPt: y, HorzAnchor: AnchorPage, VertAnchor: AnchorPage, Wrap: wrap,
	}}}
}

func TestRender_FloatingPictureIsPlacedOffTheLine(t *testing.T) {
	f := &fakeRenderer{}
	p := para(AlignLeft, false, floatingPicture(300, 400, 50, 30, WrapNone), run("text", 12))
	ConvertToPdf(&Document{Body: bodyOf(p)}).render(f)

	if want := (imageCall{page: 1, x: 300, y: 400, w: 50, h: 30, name: "f"}); len(f.images) != 1 || f.images[0] != want {
		t.Errorf("images = %+v, want %+v", f.images, want)
	}
	if got := drawnX(t, f, "text"); got != testPage.originX {
		t.Errorf("text x = %.1f, want %.1f: a floating picture takes no room in its line", got, testPage.originX)
	}
}

func TestRender_TextWrapsAroundAFloatingPicture(t *testing.T) {
	// A picture on the left edge of the text, two lines high.
	pic := floatingPicture(testPage.originX, testPage.originY, 100, 2*12*lineSpacing, WrapAround)
	tests := []struct {
		name  string
		wrap  Wrap
		wantX float64 // where the first line starts
		wantY float64 // its baseline
	}{
		{"around", WrapAround, testPage.originX + 100, testPage.originY + 12},
		{"top and bottom", WrapTopAndBottom, testPage.originX, testPage.originY + 2*12*lineSpacing + 12},
		{"none", WrapNone, testPage.originX, testPage.originY + 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pic.Image.Float.Wrap = tt.wrap
			f := &fakeRenderer{}
			ConvertToPdf(&Document{Body: bodyOf(para(AlignLeft, false, pic, run("first", 12)))}).render(f)
			d := drawsOf(f, "first")[0]
			if d.x != tt.wantX || d.y != tt.wantY {
				t.Errorf("first line at (%.1f, %.1f), want (%.1f, %.1f)", d.x, d.y, tt.wantX, tt.wantY)
			}
		})
	}
}

func TestRender_LinesBesideAFloatAreNarrowed(t *testing.T) {
	// A picture covering the right half of the text, three lines high: the
	// lines beside it wrap at half the width, the ones below at the full width.
	half := contentWidthPt / 2
	pic := floatingPicture(testPage.originX+half, testPage.originY, half, 3*12*lineSpacing, WrapAround)
	words := make([]Run, 0, 80)
	for range 80 {
		words = append(words, run("ab ", 12))
	}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: bodyOf(para(AlignLeft, false, append([]Run{pic}, words...)...))}).render(f)

	right := make(map[float64]float64) // baseline → right end of its last word
	for _, d := range f.draws {
		right[d.y] = max(right[d.y], d.x+12)
	}
	for y, end := range right {
		beside := y < testPage.originY+3*12*lineSpacing
		if beside && end > testPage.originX+half {
			t.Errorf("line at %.1f ends at %.1f, inside the picture starting at %.1f", y, end, testPage.originX+half)
		}
		if !beside && end <= testPage.originX+half {
			t.Errorf("line at %.1f ends at %.1f, but below the picture it has the full width", y, end)
		}
	}
}

func TestRender_TextWrapsAroundAFloatingTable(t *testing.T) {
	tbl := Table{
		ColumnWidths: []float64{100},
		Rows:         []Row{{Cells: []Cell{{ColSpan: 1, Paragraphs: []Paragraph{para(AlignLeft, false, run("T", 12))}}}}},
		Float:        &Float{DistRightPt: 10},
	}
	body := []BodyElement{{Table: &tbl}, {Paragraph: &Paragraph{Runs: []Run{run("beside", 12)}}}}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: body}).render(f)

	if got, want := drawnX(t, f, "beside"), testPage.originX+110; got != want {
		t.Errorf("text beside the table at x = %.1f, want %.1f (past the table and its clearance)", got, want)
	}
}

func TestRender_FloatingTableThatDoesNotFitMovesOn(t *testing.T) {
	cell := Cell{ColSpan: 1, Paragraphs: []Paragraph{para(AlignLeft, false, run("T", 12))}}
	tbl := Table{ColumnWidths: []float64{100}, Rows: []Row{{Cells: []Cell{cell}}, {Cells: []Cell{cell}}}, Float: &Float{}}
	body := append(lines(linesPerFrame()-1, "x"), BodyElement{Table: &tbl})
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: body}).render(f)

	for _, d := range drawsOf(f, "T") {
		if d.page != 2 {
			t.Errorf("floating table row drawn on page %d, want 2 (moved whole)", d.page)
		}
	}
}

func TestRender_FloatingTableTallerThanAPageFlows(t *testing.T) {
	cell := Cell{ColSpan: 1, Paragraphs: []Paragraph{para(AlignLeft, false, run("T", 12))}}
	rows := make([]Row, linesPerFrame()+5)
	for i := range rows {
		rows[i] = Row{Cells: []Cell{cell}}
	}
	tbl := Table{ColumnWidths: []float64{100}, Rows: rows, Float: &Float{}}
	f := &fakeRenderer{}
	ConvertToPdf(&Document{Body: []BodyElement{{Table: &tbl}}}).render(f)

	if f.page != 2 {
		t.Errorf("pages = %d, want 2: the table paginates like a flowing one", f.page)
	}
}
