package fonts_test

import (
	"context"
	"log"
	"time"

	"github.com/h0rn3t/docx2pdf/document"
	"github.com/h0rn3t/docx2pdf/document/fonts"
)

// Microsoft's fonts are downloaded once, when the program starts, and the
// directory is then handed to every conversion.
func ExampleDownloadMicrosoftFonts() {
	fontDir, err := fonts.DefaultDir()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := fonts.DownloadMicrosoftFonts(ctx, fontDir); err != nil {
		// Not fatal: a font that did not arrive is drawn in its replacement.
		log.Printf("some Microsoft fonts were not downloaded: %v", err)
	}

	doc, err := document.Open("in.docx")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = doc.Close() }() // read-only

	// Arial, Times New Roman, Verdana, Tahoma, ... are now drawn in themselves.
	if err := document.ConvertToPdf(doc).WithFontDir(fontDir).WriteToFile("out.pdf"); err != nil {
		log.Fatal(err)
	}
}
