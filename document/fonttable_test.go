package document

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/h0rn3t/docx2pdf/document/fonts"
	"github.com/h0rn3t/docx2pdf/internal/docxtest"
)

const testFontKey = "{0A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9}"

// obfuscated is data as Word stores an embedded font under testFontKey.
func obfuscated(data []byte) string {
	out := bytes.Clone(data)
	if !deobfuscateFont(out, testFontKey) { // the XOR is its own inverse
		panic("bad test key")
	}
	return string(out)
}

func TestOpen_EmbeddedFontsAreDeobfuscated(t *testing.T) {
	mono := fonts.Bytes(fonts.Mono, false, false)
	doc := openFixture(t, docxtest.BuildWith(t, `<w:p><w:r><w:t>a</w:t></w:r></w:p>`, map[string]string{
		"word/_rels/document.xml.rels": docxtest.Rels(docxtest.Rel("rId9", "fontTable", "fontTable.xml")),
		"word/fontTable.xml": `<w:fonts xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
			`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<w:font w:name="House Font"><w:embedRegular r:id="rId1" w:fontKey="` + testFontKey + `"/>` +
			`<w:embedBold r:id="rId2" w:fontKey="` + testFontKey + `"/></w:font></w:fonts>`,
		"word/_rels/fontTable.xml.rels": docxtest.Rels(docxtest.Rel("rId1", "font", "fonts/font1.odttf") +
			docxtest.Rel("rId2", "font", "fonts/font2.odttf")),
		"word/fonts/font1.odttf": obfuscated(mono),
		"word/fonts/font2.odttf": "not a font, once deobfuscated or not, padded to 32+ bytes",
	}))

	if len(doc.Fonts) != 1 {
		t.Fatalf("Fonts = %d, want 1 (the corrupt bold face skipped)", len(doc.Fonts))
	}
	if f := doc.Fonts[0]; f.Family != "House Font" || f.Bold || f.Italic || !bytes.Equal(f.Data, mono) {
		t.Errorf("font = %q bold=%v italic=%v, %d bytes; want the regular House Font, deobfuscated", f.Family, f.Bold, f.Italic, len(f.Data))
	}
}

func TestRenderer_RealFontIsUsedWhereItHasTheGlyphs(t *testing.T) {
	mono := fonts.Bytes(fonts.Mono, false, false)
	r := newFPDFRenderer(testGeometry.WidthPt, testGeometry.HeightPt, []Font{{Family: "Calibri", Data: mono}}, nil)
	r.SetFont("Calibri", false, false, false, 10)

	// Liberation Mono's glyphs are all 0.6 em wide; Carlito's "i" is far
	// narrower, so the width tells which font measured it.
	if got := r.TextWidth("iiii"); got != 24 {
		t.Errorf("TextWidth(iiii) in the embedded Calibri = %.2f, want 24 (the embedded monospace face)", got)
	}
	if got := r.TextWidth("i中"); got == 12 {
		t.Errorf("TextWidth(i中) = %.2f: a string the font has no glyph for must fall back to the replacement", got)
	}
}

func TestRenderer_BrokenFontIsNotRegistered(t *testing.T) {
	broken := append([]byte{0, 1, 0, 0}, bytes.Repeat([]byte{0xff}, 64)...) // a TrueType tag, garbage after
	r := newFPDFRenderer(testGeometry.WidthPt, testGeometry.HeightPt, []Font{{Family: "Bad", Data: broken}}, nil)
	r.SetFont("Bad", false, false, false, 10)
	r.AddPage(testGeometry.WidthPt, testGeometry.HeightPt)
	r.DrawText(10, 10, "text")
	if err := r.Output(&bytes.Buffer{}); err != nil {
		t.Errorf("Output() error = %v, want nil (the broken font replaced)", err)
	}
}

func TestFontIndex_FindsInstalledFacesByFamilyAndStyle(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"a.ttf":      fonts.Bytes(fonts.Serif, false, false),
		"sub/b.TTF":  fonts.Bytes(fonts.Serif, true, true),
		"notes.txt":  []byte("not a font"),
		"broken.ttf": []byte("not a font either"),
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ix := newFontIndex([]string{dir, filepath.Join(dir, "missing")})

	if got := ix.lookup(newFaceKey("Liberation Serif", true, true)); !bytes.Equal(got, fonts.Bytes(fonts.Serif, true, true)) {
		t.Errorf("lookup(Liberation Serif bold italic) = %d bytes, want the file in sub/", len(got))
	}
	if got := ix.lookup(newFaceKey("liberation serif", true, false)); got != nil {
		t.Errorf("lookup(Liberation Serif bold) = %d bytes, want none installed", len(got))
	}
}
