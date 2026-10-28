package document_test

import (
	"strings"
	"testing"

	"github.com/h0rn3t/docx2pdf/document"
)

// runTexts is the text of each of p's runs, in order.
func runTexts(p document.Paragraph) []string {
	var texts []string
	for _, r := range p.Runs {
		texts = append(texts, r.Text)
	}
	return texts
}

// --- Strikethrough (w:strike / w:dstrike) ---

func TestRunStrike(t *testing.T) {
	tests := []struct {
		name string
		rPr  string
		want bool
	}{
		{"strike", `<w:strike/>`, true},
		{"double strike", `<w:dstrike/>`, true},
		{"strike off", `<w:strike w:val="0"/>`, false},
		{"none", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `<w:p><w:r><w:rPr>` + tt.rPr + `</w:rPr><w:t>x</w:t></w:r></w:p>`
			doc := mustOpen(t, body)

			if got := paragraphs(t, doc)[0].Runs[0].Props.Strike; got != tt.want {
				t.Errorf("rPr %q: Strike = %v, want %v", tt.rPr, got, tt.want)
			}
		})
	}
}

func TestRunStrike_FromCharacterStyle(t *testing.T) {
	body := `<w:p><w:r><w:rPr><w:rStyle w:val="Gone"/></w:rPr><w:t>x</w:t></w:r></w:p>`
	styles := `<w:style w:type="character" w:styleId="Gone"><w:rPr><w:strike/></w:rPr></w:style>`
	doc := mustOpenWithStyles(t, body, styles)

	if !paragraphs(t, doc)[0].Runs[0].Props.Strike {
		t.Errorf("Strike = false, want true (inherited from the character style)")
	}
}

// --- Hidden text (w:vanish) ---

func TestRunVanish(t *testing.T) {
	tests := []struct {
		name string
		rPr  string
		want []string
	}{
		{"hidden", `<w:vanish/>`, []string{"a", "c"}}, // the tab and break go too
		{"hidden off", `<w:vanish w:val="false"/>`, []string{"a", "b", "", "", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `<w:p><w:r><w:t>a</w:t></w:r>` +
				`<w:r><w:rPr>` + tt.rPr + `</w:rPr><w:t>b</w:t><w:tab/><w:br/></w:r>` +
				`<w:r><w:t>c</w:t></w:r></w:p>`
			doc := mustOpen(t, body)

			got := runTexts(paragraphs(t, doc)[0])
			if len(got) != len(tt.want) {
				t.Fatalf("rPr %q: runs = %q, want %q", tt.rPr, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("rPr %q: runs = %q, want %q", tt.rPr, got, tt.want)
					break
				}
			}
		})
	}
}

func TestRunVanish_FromParagraphStyle(t *testing.T) {
	body := `<w:p><w:pPr><w:pStyle w:val="Secret"/></w:pPr><w:r><w:t>hidden</w:t></w:r></w:p>`
	styles := `<w:style w:type="paragraph" w:styleId="Secret"><w:rPr><w:vanish/></w:rPr></w:style>`
	doc := mustOpenWithStyles(t, body, styles)

	if got := paragraphs(t, doc)[0].Runs; len(got) != 0 {
		t.Errorf("runs = %+v, want none (the style hides its text)", got)
	}
}

func TestRunVanish_KeepsPageField(t *testing.T) {
	// The field's code is hidden, its result is not: PAGE must still be found.
	body := `<w:p>` +
		`<w:r><w:rPr><w:vanish/></w:rPr><w:fldChar w:fldCharType="begin"/></w:r>` +
		`<w:r><w:rPr><w:vanish/></w:rPr><w:instrText>PAGE</w:instrText></w:r>` +
		`<w:r><w:rPr><w:vanish/></w:rPr><w:fldChar w:fldCharType="separate"/></w:r>` +
		`<w:r><w:t>1</w:t></w:r>` +
		`<w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>`
	doc := mustOpen(t, body)

	runs := paragraphs(t, doc)[0].Runs
	if len(runs) != 1 || runs[0].Field != document.FieldPage {
		t.Errorf("runs = %+v, want one PAGE field run", runs)
	}
}

// --- All caps and small caps (w:caps / w:smallCaps) ---

func TestRunCaps(t *testing.T) {
	body := `<w:p><w:r><w:rPr><w:caps/></w:rPr><w:t>Hello, світ</w:t></w:r></w:p>`
	doc := mustOpen(t, body)

	if got := runTexts(paragraphs(t, doc)[0]); len(got) != 1 || got[0] != "HELLO, СВІТ" {
		t.Errorf("caps runs = %q, want [\"HELLO, СВІТ\"]", got)
	}
}

func TestRunSmallCaps(t *testing.T) {
	body := `<w:p><w:r><w:rPr><w:smallCaps/><w:sz w:val="20"/></w:rPr><w:t>Big data</w:t></w:r></w:p>`
	doc := mustOpen(t, body)

	runs := paragraphs(t, doc)[0].Runs
	want := []struct {
		text  string
		small bool
	}{{"B", false}, {"IG", true}, {" ", false}, {"DATA", true}}
	if len(runs) != len(want) {
		t.Fatalf("small caps runs = %q, want %d runs", runTexts(paragraphs(t, doc)[0]), len(want))
	}
	for i, w := range want {
		if runs[i].Text != w.text {
			t.Errorf("run %d text = %q, want %q", i, runs[i].Text, w.text)
		}
		if small := runs[i].Props.SizePt < 10; small != w.small {
			t.Errorf("run %d (%q) size = %v, want small = %v of 10pt", i, runs[i].Text, runs[i].Props.SizePt, w.small)
		}
	}
}

func TestRunCaps_OverridesSmallCaps(t *testing.T) {
	body := `<w:p><w:r><w:rPr><w:caps/><w:smallCaps/></w:rPr><w:t>ab</w:t></w:r></w:p>`
	doc := mustOpen(t, body)

	runs := paragraphs(t, doc)[0].Runs
	if len(runs) != 1 || runs[0].Text != "AB" || runs[0].Props.SizePt != 11 {
		t.Errorf("runs = %+v, want one full-size \"AB\" run", runs)
	}
}

// --- Symbols (w:sym) ---

func TestRunSym(t *testing.T) {
	tests := []struct {
		name, sym  string
		wantText   string
		wantFamily string
	}{
		{"private use code", `<w:sym w:font="Wingdings" w:char="F0FC"/>`, "\uF0FC", "Wingdings"},
		{"single-byte code", `<w:sym w:font="Symbol" w:char="B7"/>`, "\uF0B7", "Symbol"},
		{"no font keeps the run's", `<w:sym w:char="F0FC"/>`, "\uF0FC", "Calibri"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := mustOpen(t, `<w:p><w:r>`+tt.sym+`</w:r></w:p>`)

			runs := paragraphs(t, doc)[0].Runs
			if len(runs) != 1 || runs[0].Text != tt.wantText || runs[0].Props.FontFamily != tt.wantFamily {
				t.Errorf("%s: runs = %+v, want one %q run in %s", tt.sym, runs, tt.wantText, tt.wantFamily)
			}
		})
	}
}

func TestRunSym_BadCodeIsSkipped(t *testing.T) {
	doc := mustOpen(t, `<w:p><w:r><w:t>a</w:t><w:sym w:font="Symbol" w:char="zz"/></w:r></w:p>`)

	if got := runTexts(paragraphs(t, doc)[0]); len(got) != 1 || got[0] != "a" {
		t.Errorf("runs = %q, want [\"a\"]", got)
	}
}

// --- Contextual spacing (w:contextualSpacing) ---

func TestContextualSpacing(t *testing.T) {
	const styles = `<w:style w:type="paragraph" w:styleId="List">` +
		`<w:pPr><w:spacing w:before="200" w:after="200"/><w:contextualSpacing/></w:pPr></w:style>` +
		`<w:style w:type="paragraph" w:styleId="Plain">` +
		`<w:pPr><w:spacing w:before="200" w:after="200"/></w:pPr></w:style>`
	para := func(style, text string) string {
		return `<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr><w:r><w:t>` + text + `</w:t></w:r></w:p>`
	}
	tests := []struct {
		name string
		body string
		// want is each paragraph's {before, after} spacing, in points.
		want [][2]float64
	}{
		{
			name: "same style",
			body: para("List", "a") + para("List", "b") + para("List", "c"),
			want: [][2]float64{{10, 0}, {0, 0}, {0, 10}},
		},
		{
			name: "different style",
			body: para("List", "a") + para("Plain", "b"),
			want: [][2]float64{{10, 10}, {10, 10}},
		},
		{
			name: "only own flag counts",
			body: para("Plain", "a") + para("Plain", "b"),
			want: [][2]float64{{10, 10}, {10, 10}},
		},
		{
			name: "inline flag off",
			body: para("List", "a") +
				`<w:p><w:pPr><w:pStyle w:val="List"/><w:contextualSpacing w:val="0"/></w:pPr><w:r><w:t>b</w:t></w:r></w:p>`,
			want: [][2]float64{{10, 0}, {10, 10}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := mustOpenWithStyles(t, tt.body, styles)

			paras := paragraphs(t, doc)
			if len(paras) != len(tt.want) {
				t.Fatalf("got %d paragraphs, want %d", len(paras), len(tt.want))
			}
			for i, w := range tt.want {
				sp := paras[i].Props.Spacing
				if sp.BeforePt != w[0] || sp.AfterPt != w[1] {
					t.Errorf("paragraph %d spacing = before %v after %v, want before %v after %v",
						i, sp.BeforePt, sp.AfterPt, w[0], w[1])
				}
			}
		})
	}
}

func TestContextualSpacing_TableBetween(t *testing.T) {
	styles := `<w:style w:type="paragraph" w:styleId="List">` +
		`<w:pPr><w:spacing w:before="200" w:after="200"/><w:contextualSpacing/></w:pPr></w:style>`
	p := `<w:p><w:pPr><w:pStyle w:val="List"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`
	body := p + `<w:tbl><w:tr><w:tc><w:p/></w:tc></w:tr></w:tbl>` + p
	doc := mustOpenWithStyles(t, body, styles)

	first, last := doc.Body[0].Paragraph, doc.Body[2].Paragraph
	if first.Props.Spacing.AfterPt != 10 || last.Props.Spacing.BeforePt != 10 {
		t.Errorf("spacing around the table = after %v, before %v; want 10 and 10 (not adjacent)",
			first.Props.Spacing.AfterPt, last.Props.Spacing.BeforePt)
	}
}

// --- Table alignment (w:tblPr/w:jc) ---

func TestTableAlignment(t *testing.T) {
	tests := []struct {
		name   string
		tblPr  string
		styles string
		want   document.Alignment
	}{
		{"none", ``, ``, document.AlignLeft},
		{"center", `<w:jc w:val="center"/>`, ``, document.AlignCenter},
		{"right", `<w:jc w:val="right"/>`, ``, document.AlignRight},
		{"end", `<w:jc w:val="end"/>`, ``, document.AlignRight},
		{
			"from style",
			`<w:tblStyle w:val="Centered"/>`,
			`<w:style w:type="table" w:styleId="Centered"><w:tblPr><w:jc w:val="center"/></w:tblPr></w:style>`,
			document.AlignCenter,
		},
		{
			"inline over style",
			`<w:tblStyle w:val="Centered"/><w:jc w:val="left"/>`,
			`<w:style w:type="table" w:styleId="Centered"><w:tblPr><w:jc w:val="center"/></w:tblPr></w:style>`,
			document.AlignLeft,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `<w:tbl><w:tblPr>` + tt.tblPr + `</w:tblPr><w:tr><w:tc><w:p/></w:tc></w:tr></w:tbl>`
			doc := mustOpenWithStyles(t, body, tt.styles)

			if got := singleTable(t, doc).Alignment; got != tt.want {
				t.Errorf("tblPr %q: Alignment = %v, want %v", tt.tblPr, got, tt.want)
			}
		})
	}
}

// --- Text boxes (wps:txbx, v:textbox) ---

const (
	mcNS  = `xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"`
	wpNS  = `xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"`
	aNS   = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`
	wpsNS = `xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape"`
	vNS   = `xmlns:v="urn:schemas-microsoft-com:vml"`
)

// wpsTextBox is a DrawingML text box shape holding one paragraph of text.
func wpsTextBox(text string) string {
	return `<w:drawing><wp:anchor ` + wpNS + `><wp:extent cx="914400" cy="457200"/>` +
		`<a:graphic ` + aNS + `><a:graphicData uri="http://schemas.microsoft.com/office/word/2010/wordprocessingShape">` +
		`<wps:wsp ` + wpsNS + `><wps:txbx><w:txbxContent><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:txbxContent></wps:txbx></wps:wsp>` +
		`</a:graphicData></a:graphic></wp:anchor></w:drawing>`
}

// vmlTextBox is the legacy VML text box Word writes as the fallback.
func vmlTextBox(text string) string {
	return `<w:pict><v:shape ` + vNS + ` style="width:72pt;height:36pt"><v:textbox><w:txbxContent>` +
		`<w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:txbxContent></v:textbox></v:shape></w:pict>`
}

// bodyTexts is the text of every body paragraph, in order.
func bodyTexts(t *testing.T, doc *document.Document) []string {
	t.Helper()
	var texts []string
	for _, p := range paragraphs(t, doc) {
		texts = append(texts, strings.Join(runTexts(p), ""))
	}
	return texts
}

func TestTextBox(t *testing.T) {
	tests := []struct {
		name string
		run  string
		want []string
	}{
		{
			name: "choice is used, fallback ignored",
			run: `<mc:AlternateContent ` + mcNS + `><mc:Choice Requires="wps">` + wpsTextBox("new") +
				`</mc:Choice><mc:Fallback>` + vmlTextBox("old") + `</mc:Fallback></mc:AlternateContent>`,
			want: []string{"before", "new"},
		},
		{
			name: "empty choice falls back",
			run: `<mc:AlternateContent ` + mcNS + `><mc:Choice Requires="wpc"><w:drawing/></mc:Choice>` +
				`<mc:Fallback>` + vmlTextBox("old") + `</mc:Fallback></mc:AlternateContent>`,
			want: []string{"before", "old"},
		},
		{
			name: "bare drawing",
			run:  wpsTextBox("box"),
			want: []string{"before", "box"},
		},
		{
			name: "bare VML",
			run:  vmlTextBox("box"),
			want: []string{"before", "box"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := mustOpen(t, `<w:p><w:r><w:t>before</w:t>`+tt.run+`</w:r></w:p>`)

			got := bodyTexts(t, doc)
			if len(got) != len(tt.want) || got[0] != tt.want[0] || got[1] != tt.want[1] {
				t.Errorf("paragraph texts = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTextBox_InTableCell(t *testing.T) {
	body := `<w:tbl><w:tr><w:tc><w:p><w:r>` + wpsTextBox("boxed") + `</w:r></w:p></w:tc></w:tr></w:tbl>`
	doc := mustOpen(t, body)

	paras := singleTable(t, doc).Rows[0].Cells[0].Paragraphs
	if len(paras) != 2 || len(paras[1].Runs) != 1 || paras[1].Runs[0].Text != "boxed" {
		t.Errorf("cell paragraphs = %+v, want the anchor paragraph then \"boxed\"", paras)
	}
}

func TestTextBox_SectionBreakStaysAfterIt(t *testing.T) {
	body := `<w:p><w:pPr><w:sectPr><w:pgSz w:w="12240" w:h="15840"/></w:sectPr></w:pPr><w:r>` + wpsTextBox("boxed") + `</w:r></w:p>` +
		`<w:p><w:r><w:t>next</w:t></w:r></w:p>` +
		`<w:sectPr><w:pgSz w:w="15840" w:h="12240"/></w:sectPr>`
	doc := mustOpen(t, body)

	if len(doc.Sections) != 2 || doc.Sections[0].End != 2 {
		t.Errorf("sections = %+v, want the first to end after the anchor paragraph and its box (2)", doc.Sections)
	}
}

// --- Keep with next / keep lines together (w:keepNext, w:keepLines) ---

func TestKeepFlags(t *testing.T) {
	const styles = `<w:style w:type="paragraph" w:styleId="Heading1">` +
		`<w:pPr><w:keepNext/><w:keepLines/></w:pPr></w:style>`
	tests := []struct {
		name                    string
		pPr                     string
		wantKeepNext, wantLines bool
	}{
		{"none", ``, false, false},
		{"inline", `<w:keepNext/><w:keepLines/>`, true, true},
		{"from style", `<w:pStyle w:val="Heading1"/>`, true, true},
		{"inline off over style", `<w:pStyle w:val="Heading1"/><w:keepNext w:val="0"/>`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := mustOpenWithStyles(t, `<w:p><w:pPr>`+tt.pPr+`</w:pPr><w:r><w:t>x</w:t></w:r></w:p>`, styles)

			props := paragraphs(t, doc)[0].Props
			if props.KeepNext != tt.wantKeepNext || props.KeepLines != tt.wantLines {
				t.Errorf("pPr %q: KeepNext, KeepLines = %v, %v; want %v, %v",
					tt.pPr, props.KeepNext, props.KeepLines, tt.wantKeepNext, tt.wantLines)
			}
		})
	}
}
