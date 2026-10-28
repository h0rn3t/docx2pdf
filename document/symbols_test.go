package document

import (
	"testing"

	"golang.org/x/image/font/sfnt"

	"github.com/h0rn3t/docx2pdf/document/fonts"
)

func TestSymbolText(t *testing.T) {
	tests := []struct {
		family, in, want string
	}{
		{"Symbol", "\uF0B7", "•"},               // Word's default bullet
		{"Symbol", "\uF061\uF062\uF070", "αβπ"}, // Greek by the Latin key
		{"Symbol", "\uF0A3 \uF0B3", "≤ ≥"},
		{"symbol", "\uF031", "1"}, // digits keep their place
		{"Wingdings", "\uF0A7", "▪"},
		{"Wingdings", "\uF0FC", "√"}, // check mark
		{"Wingdings", "\uF0A8", "□"}, // check box
		{"Wingdings", "\uF0D8", "►"},
		{"Wingdings", "\uF0B7", "\uF0B7"}, // a code with no stand-in stays
		{"Calibri", "\uF0B7", "\uF0B7"},   // not a symbol font
		{"Symbol", "x\u00B7", "ξ•"},       // single-byte codes, as older files and bullets store them
		{"Wingdings", "§", "▪"},           // the usual Wingdings bullet
		{"Wingdings", "\u0400", "\u0400"}, // past the font's 256 codes
	}
	for _, tt := range tests {
		if got := symbolText(tt.family, tt.in); got != tt.want {
			t.Errorf("symbolText(%q, %q) = %q, want %q", tt.family, tt.in, got, tt.want)
		}
	}
}

// Every stand-in must exist in the replacement font a symbol font falls back
// to, or substituting it would trade one missing glyph for another.
func TestSymbolText_StandInsAreInTheReplacementFont(t *testing.T) {
	for family, chars := range symbolChars {
		replacement := mapFontFamily(family)
		f, err := sfnt.Parse(fonts.Bytes(replacement, false, false))
		if err != nil {
			t.Fatalf("parse %s: %v", replacement, err)
		}
		var buf sfnt.Buffer
		for code, s := range chars {
			for _, c := range s {
				if i, err := f.GlyphIndex(&buf, c); err != nil || i == 0 {
					t.Errorf("%s U+%04X: stand-in %q is missing from %s", family, code, s, replacement)
				}
			}
		}
	}
}

func TestFPDFRenderer_DrawsSymbolFontWithoutTheFont(t *testing.T) {
	r := newFPDFRenderer(testGeometry.WidthPt, testGeometry.HeightPt, nil, nil)
	r.AddPage(testGeometry.WidthPt, testGeometry.HeightPt)
	r.SetFont(fonts.Sans, false, false, false, false, 12)
	want := r.TextWidth("•")

	r.SetFont("Symbol", false, false, false, false, 12)
	if got := r.TextWidth("\uF0B7"); got != want {
		t.Errorf("TextWidth(U+F0B7) in Symbol = %v, want %v (the width of •)", got, want)
	}
}
