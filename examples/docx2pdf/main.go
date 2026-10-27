// Command docx2pdf converts a .docx file to PDF.
//
// Usage:
//
//	docx2pdf [-system-fonts] <input.docx> <output.pdf>
//
// -system-fonts draws text in the fonts installed on this machine when the
// document asks for one, instead of the bundled metric-compatible replacements.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/h0rn3t/docx2pdf/document"
)

func main() {
	systemFonts := flag.Bool("system-fonts", false, "use the fonts installed on this machine")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s [-system-fonts] <input.docx> <output.pdf>\n", os.Args[0])
		os.Exit(2)
	}
	in, out := flag.Arg(0), flag.Arg(1)

	doc, err := document.Open(in)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = doc.Close() }()

	conv := document.ConvertToPdf(doc)
	if *systemFonts {
		conv.WithSystemFonts()
	}
	if err := conv.WriteToFile(out); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s\n", out)
}
