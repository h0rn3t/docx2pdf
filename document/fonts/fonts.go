// Package fonts embeds metric-compatible replacement families so convert can
// render Unicode text - Cyrillic included - without relying on the PDF core
// fonts (Latin-only) or on any font installed on the host. All are SIL OFL 1.1
// licensed (see the LICENSE-*.txt files):
//
//   - Liberation Sans/Serif/Mono - metric-compatible with Arial/Times New
//     Roman/Courier New, the legacy Word core fonts.
//   - Carlito - metric-compatible with Calibri, Word's default body font
//     (minorHAnsi) since Office 2007.
//
// Each family carries Regular/Bold/Italic/BoldItalic variants and covers
// Cyrillic (see fonts_test.go) - the reason Cambria is not paired with its
// metric clone Caladea, which is Latin/Greek-only and would render Cyrillic as
// blank .notdef boxes; Cambria maps to Liberation Serif instead.
package fonts

import _ "embed"

//go:embed LiberationSans-Regular.ttf
var sansRegular []byte

//go:embed LiberationSans-Bold.ttf
var sansBold []byte

//go:embed LiberationSans-Italic.ttf
var sansItalic []byte

//go:embed LiberationSans-BoldItalic.ttf
var sansBoldItalic []byte

//go:embed LiberationSerif-Regular.ttf
var serifRegular []byte

//go:embed LiberationSerif-Bold.ttf
var serifBold []byte

//go:embed LiberationSerif-Italic.ttf
var serifItalic []byte

//go:embed LiberationSerif-BoldItalic.ttf
var serifBoldItalic []byte

//go:embed LiberationMono-Regular.ttf
var monoRegular []byte

//go:embed LiberationMono-Bold.ttf
var monoBold []byte

//go:embed LiberationMono-Italic.ttf
var monoItalic []byte

//go:embed LiberationMono-BoldItalic.ttf
var monoBoldItalic []byte

//go:embed Carlito-Regular.ttf
var carlitoRegular []byte

//go:embed Carlito-Bold.ttf
var carlitoBold []byte

//go:embed Carlito-Italic.ttf
var carlitoItalic []byte

//go:embed Carlito-BoldItalic.ttf
var carlitoBoldItalic []byte

// Sans, Serif, Mono, and Carlito are the embedded font family names, matching
// the names AddUTF8FontFromBytes is registered under.
const (
	Sans    = "Liberation Sans"
	Serif   = "Liberation Serif"
	Mono    = "Liberation Mono"
	Carlito = "Carlito" // metric-compatible with Calibri
)

// Families lists every embedded font family name.
var Families = []string{Sans, Serif, Mono, Carlito}

// Bytes returns the embedded TTF bytes for family's bold/italic variant.
// It panics if family is not one of Sans, Serif, Mono, or Carlito.
func Bytes(family string, bold, italic bool) []byte {
	switch family {
	case Sans:
		return pick(sansRegular, sansBold, sansItalic, sansBoldItalic, bold, italic)
	case Serif:
		return pick(serifRegular, serifBold, serifItalic, serifBoldItalic, bold, italic)
	case Mono:
		return pick(monoRegular, monoBold, monoItalic, monoBoldItalic, bold, italic)
	case Carlito:
		return pick(carlitoRegular, carlitoBold, carlitoItalic, carlitoBoldItalic, bold, italic)
	default:
		panic("docx2pdf: unknown embedded font family " + family)
	}
}

func pick(regular, b, i, bi []byte, bold, italic bool) []byte {
	switch {
	case bold && italic:
		return bi
	case bold:
		return b
	case italic:
		return i
	default:
		return regular
	}
}
