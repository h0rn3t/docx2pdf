package document

import (
	"bytes"
	"testing"

	"github.com/h0rn3t/docx2pdf/document/fonts"
)

func TestMapFontFamily(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Times New Roman", fonts.Serif},
		{"Georgia", fonts.Serif},
		{"Garamond", fonts.Serif},
		{"Calibri", fonts.Carlito},
		{"Calibri Light", fonts.Carlito},
		{"Cambria", fonts.Serif},
		{"Cambria Math", fonts.Serif},
		{"Arial", fonts.Sans},
		{"", fonts.Sans},
		{"Courier New", fonts.Mono},
		{"Consolas", fonts.Mono},
		{"Menlo", fonts.Mono},
		{"Constantia", fonts.Serif},
		{"Palatino Linotype", fonts.Serif},
		{"Bookman Old Style", fonts.Serif},
		{"Rockwell", fonts.Serif},
		{"Monaco", fonts.Mono},
		{"Fira Code", fonts.Mono},
		{"Cascadia Code", fonts.Mono},
		{"JetBrains Mono", fonts.Mono},
		{"Microsoft Sans Serif", fonts.Sans},
		{"Fira Sans", fonts.Sans},
		{"DejaVu Sans Mono", fonts.Mono},
	}
	for _, tt := range tests {
		if got := mapFontFamily(tt.name); got != tt.want {
			t.Errorf("mapFontFamily(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestStrikeDrawsALineThroughText(t *testing.T) {
	// fpdf draws a strikethrough (like an underline) as a filled rectangle.
	rects := func(underline, strike bool) int {
		r := newFPDFRenderer(testGeometry.WidthPt, testGeometry.HeightPt, nil, nil)
		r.pdf.SetCompression(false)
		r.AddPage(testGeometry.WidthPt, testGeometry.HeightPt)
		r.SetFont(fonts.Sans, false, false, underline, strike, 12)
		r.DrawText(marginPt, 120, "hello")
		var buf bytes.Buffer
		if err := r.Output(&buf); err != nil {
			t.Fatalf("Output: %v", err)
		}
		return bytes.Count(buf.Bytes(), []byte(" re f"))
	}
	plain := rects(false, false)
	if got := rects(false, true); got != plain+1 {
		t.Errorf("strike: %d filled rectangles, want %d", got, plain+1)
	}
	if got := rects(true, true); got != plain+2 {
		t.Errorf("underline+strike: %d filled rectangles, want %d", got, plain+2)
	}
}

func TestTrivialRenderEmitsPDF(t *testing.T) {
	r := newFPDFRenderer(testGeometry.WidthPt, testGeometry.HeightPt, nil, nil)
	r.AddPage(testGeometry.WidthPt, testGeometry.HeightPt)
	r.SetFont(fonts.Sans, true, false, true, false, 12)
	r.DrawText(marginPt, 120, "hello")

	var buf bytes.Buffer
	if err := r.Output(&buf); err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Fatalf("output does not start with %%PDF-: %q", buf.Bytes()[:min(8, buf.Len())])
	}
}
