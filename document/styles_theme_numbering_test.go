package document_test

import (
	"io"
	"testing"

	"github.com/h0rn3t/docx2pdf/document"
	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

// mustOpenWith opens a fixture built from bodyXML plus the given optional parts
// (styles/theme/numbering), failing on error.
func mustOpenWith(t *testing.T, bodyXML string, extra map[string]string) *document.Document {
	t.Helper()
	doc, err := document.Open(docxtest.BuildWith(t, bodyXML, extra))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = doc.Close() })
	return doc
}

// --- Paragraph styles (A1) ---

func TestParaStyle_AlignmentInheritedViaBasedOn(t *testing.T) {
	styles := docxtest.Styles(`
		<w:style w:type="paragraph" w:styleId="Parent"><w:pPr><w:jc w:val="center"/></w:pPr></w:style>
		<w:style w:type="paragraph" w:styleId="Child"><w:basedOn w:val="Parent"/></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:pPr><w:pStyle w:val="Child"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if got := paragraphs(t, doc)[0].Props.Alignment; got != document.AlignCenter {
		t.Fatalf("alignment = %v, want AlignCenter (inherited via basedOn)", got)
	}
}

func TestParaStyle_InlineOverridesStyle(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="paragraph" w:styleId="S"><w:pPr><w:jc w:val="center"/></w:pPr></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:pPr><w:pStyle w:val="S"/><w:jc w:val="right"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if got := paragraphs(t, doc)[0].Props.Alignment; got != document.AlignRight {
		t.Fatalf("alignment = %v, want AlignRight (inline wins)", got)
	}
}

func TestParaStyle_AlignmentFromDocDefaults(t *testing.T) {
	styles := docxtest.Styles(`
		<w:docDefaults><w:pPrDefault><w:pPr><w:jc w:val="center"/></w:pPr></w:pPrDefault></w:docDefaults>
		<w:style w:type="paragraph" w:styleId="S"></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:pPr><w:pStyle w:val="S"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if got := paragraphs(t, doc)[0].Props.Alignment; got != document.AlignCenter {
		t.Fatalf("alignment = %v, want AlignCenter (docDefaults under style)", got)
	}
}

func TestParaStyle_IndentFromStyle(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="paragraph" w:styleId="S"><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:pPr><w:pStyle w:val="S"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	ind := paragraphs(t, doc)[0].Props.Indent
	if ind.LeftPt != 36 || ind.FirstLineOffsetPt != -18 {
		t.Fatalf("indent = %+v, want LeftPt 36 FirstLineOffsetPt -18", ind)
	}
}

// --- Run styles (A2) ---

func TestRunStyle_BoldFromParagraphStyle(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="paragraph" w:styleId="S"><w:rPr><w:b/></w:rPr></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:pPr><w:pStyle w:val="S"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if !paragraphs(t, doc)[0].Runs[0].Props.Bold {
		t.Fatal("run should be bold from paragraph style rPr")
	}
}

func TestRunStyle_ItalicFromCharacterStyle(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="character" w:styleId="Emph"><w:rPr><w:i/></w:rPr></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:r><w:rPr><w:rStyle w:val="Emph"/></w:rPr><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if !paragraphs(t, doc)[0].Runs[0].Props.Italic {
		t.Fatal("run should be italic from character style rPr")
	}
}

func TestRunStyle_SizeFromRPrDefault(t *testing.T) {
	styles := docxtest.Styles(`<w:docDefaults><w:rPrDefault><w:rPr><w:sz w:val="28"/></w:rPr></w:rPrDefault></w:docDefaults>`)
	doc := mustOpenWith(t, `<w:p><w:r><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if got := paragraphs(t, doc)[0].Runs[0].Props.SizePt; got != 14 {
		t.Fatalf("size = %v, want 14 (from rPrDefault sz=28 half-points)", got)
	}
}

func TestRunStyle_ExplicitToggleOffWinsOverStyle(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="character" w:styleId="B"><w:rPr><w:b/></w:rPr></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:r><w:rPr><w:rStyle w:val="B"/><w:b w:val="false"/></w:rPr><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if paragraphs(t, doc)[0].Runs[0].Props.Bold {
		t.Fatal("explicit w:b=false on the run should win over the character style")
	}
}

func TestRunStyle_InlineWinsOverStyle(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="paragraph" w:styleId="S"><w:rPr><w:sz w:val="40"/></w:rPr></w:style>`)
	doc := mustOpenWith(t, `<w:p><w:pPr><w:pStyle w:val="S"/></w:pPr><w:r><w:rPr><w:sz w:val="20"/></w:rPr><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/styles.xml": styles})
	if got := paragraphs(t, doc)[0].Runs[0].Props.SizePt; got != 10 {
		t.Fatalf("size = %v, want 10 (inline sz=20 wins over style sz=40)", got)
	}
}

// --- Theme colors (A3) ---

func TestColor_ThemeReferenceResolvesToHex(t *testing.T) {
	theme := docxtest.Theme(`<a:accent1><a:srgbClr val="FF0000"/></a:accent1>`)
	doc := mustOpenWith(t, `<w:p><w:r><w:rPr><w:color w:themeColor="accent1"/></w:rPr><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/theme/theme1.xml": theme})
	if got := paragraphs(t, doc)[0].Runs[0].Props.Color; got != "#FF0000" {
		t.Fatalf("color = %q, want #FF0000 (theme accent1)", got)
	}
}

func TestColor_PlainHexValue(t *testing.T) {
	doc := mustOpenWith(t, `<w:p><w:r><w:rPr><w:color w:val="00ff00"/></w:rPr><w:t>x</w:t></w:r></w:p>`, nil)
	if got := paragraphs(t, doc)[0].Runs[0].Props.Color; got != "#00FF00" {
		t.Fatalf("color = %q, want #00FF00", got)
	}
}

func TestColor_AutoIsEmpty(t *testing.T) {
	doc := mustOpenWith(t, `<w:p><w:r><w:rPr><w:color w:val="auto"/></w:rPr><w:t>x</w:t></w:r></w:p>`, nil)
	if got := paragraphs(t, doc)[0].Runs[0].Props.Color; got != "" {
		t.Fatalf("color = %q, want empty for auto", got)
	}
}

func TestColor_ThemeReferenceWithoutThemePart(t *testing.T) {
	doc := mustOpenWith(t, `<w:p><w:r><w:rPr><w:color w:themeColor="accent1"/></w:rPr><w:t>x</w:t></w:r></w:p>`, nil)
	if got := paragraphs(t, doc)[0].Runs[0].Props.Color; got != "" {
		t.Fatalf("color = %q, want empty when theme1.xml is absent", got)
	}
}

func TestColor_UnknownThemeSlotIsEmpty(t *testing.T) {
	theme := docxtest.Theme(`<a:accent2><a:srgbClr val="FF0000"/></a:accent2>`)
	doc := mustOpenWith(t, `<w:p><w:r><w:rPr><w:color w:themeColor="accent1"/></w:rPr><w:t>x</w:t></w:r></w:p>`,
		map[string]string{"word/theme/theme1.xml": theme})
	if got := paragraphs(t, doc)[0].Runs[0].Props.Color; got != "" {
		t.Fatalf("color = %q, want empty for a theme slot not in the scheme", got)
	}
}

// --- Numbering (A4) ---

// simpleNumbering is one decimal list (numId=1) with a nested second level.
const simpleNumbering = `
	<w:abstractNum w:abstractNumId="0">
		<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/>
			<w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl>
		<w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1.%2."/></w:lvl>
	</w:abstractNum>
	<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`

func markerText(t *testing.T, p document.Paragraph) string {
	t.Helper()
	if len(p.Runs) == 0 {
		t.Fatal("paragraph has no runs (expected a marker run)")
	}
	return p.Runs[0].Text
}

func TestNumbering_SequentialDecimal(t *testing.T) {
	body := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>a</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>b</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>c</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, map[string]string{"word/numbering.xml": docxtest.Numbering(simpleNumbering)})
	ps := paragraphs(t, doc)
	for i, want := range []string{"1.", "2.", "3."} {
		if got := markerText(t, ps[i]); got != want {
			t.Fatalf("paragraph %d marker = %q, want %q", i, got, want)
		}
	}
}

func TestNumbering_NestedLevelReset(t *testing.T) {
	lvl := func(l int) string {
		return `<w:p><w:pPr><w:numPr><w:ilvl w:val="` + itoa(l) + `"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	}
	body := lvl(0) + lvl(1) + lvl(1) + lvl(0) + lvl(1)
	doc := mustOpenWith(t, body, map[string]string{"word/numbering.xml": docxtest.Numbering(simpleNumbering)})
	ps := paragraphs(t, doc)
	for i, want := range []string{"1.", "1.1.", "1.2.", "2.", "2.1."} {
		if got := markerText(t, ps[i]); got != want {
			t.Fatalf("paragraph %d marker = %q, want %q", i, got, want)
		}
	}
}

func TestNumbering_Bullet(t *testing.T) {
	num := `<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/><w:lvlText w:val=""/></w:lvl></w:abstractNum>
		<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`
	body := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, map[string]string{"word/numbering.xml": docxtest.Numbering(num)})
	if got := markerText(t, paragraphs(t, doc)[0]); got != "•" {
		t.Fatalf("bullet marker = %q, want %q", got, "•")
	}
}

// Word's own bullet is U+F0B7 in the Symbol font: the marker keeps the font, so
// the renderer can stand a • in for it when Symbol is not installed.
func TestNumbering_SymbolFontBulletKeepsItsFont(t *testing.T) {
	num := `<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/><w:lvlText w:val="` + "\uF0B7" + `"/>` +
		`<w:rPr><w:rFonts w:ascii="Symbol" w:hAnsi="Symbol" w:hint="default"/></w:rPr></w:lvl></w:abstractNum>
		<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`
	body := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, map[string]string{"word/numbering.xml": docxtest.Numbering(num)})

	marker := paragraphs(t, doc)[0].Runs[0]
	if marker.Text != "\uF0B7" || marker.Props.FontFamily != "Symbol" {
		t.Errorf("marker = %q in %q, want \"\\uF0B7\" in Symbol", marker.Text, marker.Props.FontFamily)
	}
}

func TestNumbering_LowerLetter(t *testing.T) {
	num := `<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="lowerLetter"/><w:lvlText w:val="%1)"/></w:lvl></w:abstractNum>
		<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`
	item := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	doc := mustOpenWith(t, item+item+item, map[string]string{"word/numbering.xml": docxtest.Numbering(num)})
	ps := paragraphs(t, doc)
	for i, want := range []string{"a)", "b)", "c)"} {
		if got := markerText(t, ps[i]); got != want {
			t.Fatalf("paragraph %d marker = %q, want %q", i, got, want)
		}
	}
}

func TestNumbering_HangingIndentFromLevel(t *testing.T) {
	body := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, map[string]string{"word/numbering.xml": docxtest.Numbering(simpleNumbering)})
	ind := paragraphs(t, doc)[0].Props.Indent
	if ind.LeftPt != 36 || ind.FirstLineOffsetPt != -18 {
		t.Fatalf("list indent = %+v, want LeftPt 36 FirstLineOffsetPt -18 (from level pPr/ind)", ind)
	}
}

func TestNumbering_NumPrFromParagraphStyle(t *testing.T) {
	styles := docxtest.Styles(`<w:style w:type="paragraph" w:styleId="L"><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr></w:style>`)
	body := `<w:p><w:pPr><w:pStyle w:val="L"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, map[string]string{
		"word/styles.xml":    styles,
		"word/numbering.xml": docxtest.Numbering(simpleNumbering),
	})
	if got := markerText(t, paragraphs(t, doc)[0]); got != "1." {
		t.Fatalf("marker = %q, want %q (numPr resolved from paragraph style)", got, "1.")
	}
}

func TestNumbering_LvlOverrideStart(t *testing.T) {
	num := simpleNumbering + `<w:num w:numId="2"><w:abstractNumId w:val="0"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="5"/></w:lvlOverride></w:num>`
	body := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, map[string]string{"word/numbering.xml": docxtest.Numbering(num)})
	if got := markerText(t, paragraphs(t, doc)[0]); got != "5." {
		t.Fatalf("marker = %q, want %q (lvlOverride start=5)", got, "5.")
	}
}

func TestNumbering_AbsentPartRendersWithoutMarker(t *testing.T) {
	body := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>only</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, nil)
	if got := markerText(t, paragraphs(t, doc)[0]); got != "only" {
		t.Fatalf("first run = %q, want %q (no marker when numbering.xml is absent)", got, "only")
	}
}

// --- Smoke render (6.1): colored run + list paragraph through the fpdf path ---

func TestSmokeRender_ColorAndList(t *testing.T) {
	body := `<w:p><w:r><w:rPr><w:color w:val="FF0000"/></w:rPr><w:t>red</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p>`
	doc := mustOpenWith(t, body, map[string]string{"word/numbering.xml": docxtest.Numbering(simpleNumbering)})
	if err := document.ConvertToPdf(doc).Write(io.Discard); err != nil {
		t.Fatalf("render colored run + list to PDF: %v", err)
	}
}

// itoa is a tiny local helper so the numbering fixtures stay readable.
func itoa(n int) string {
	return string(rune('0' + n))
}
