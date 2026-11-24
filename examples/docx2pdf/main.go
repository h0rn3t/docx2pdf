// Command docx2pdf converts a .docx file to PDF.
//
// Usage:
//
//	docx2pdf [-system-fonts] [-download-fonts] [-download-ms-fonts] [-font-dir dir] <input.docx> <output.pdf>
//
// -system-fonts draws text in the fonts installed on this machine when the
// document asks for one, instead of the bundled metric-compatible replacements.
// -download-fonts fetches the open-source stand-ins for Cambria, Georgia,
// Verdana, Tahoma and Comic Sans, plus PT Sans/Serif/Mono, Fira Sans, IBM Plex
// Serif, Lato and DejaVu, into the user's cache directory (once; later runs
// reuse them) and draws text in them. -download-ms-fonts does the same with
// Microsoft's core fonts (Arial, Times New Roman, Verdana, Georgia, Tahoma, ...),
// as winetricks does; using it means accepting Microsoft's license for them.
// -font-dir adds a directory of fonts.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/h0rn3t/docx2pdf/document"
	"github.com/h0rn3t/docx2pdf/document/fonts"
)

func main() {
	systemFonts := flag.Bool("system-fonts", false, "use the fonts installed on this machine")
	downloadFonts := flag.Bool("download-fonts", false, "download open-source stand-ins for common Word fonts and use them")
	msFonts := flag.Bool("download-ms-fonts", false, "download Microsoft's core fonts (accepting their EULA) and use them")
	fontDir := flag.String("font-dir", "", "also look for fonts in this directory")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s [-system-fonts] [-download-fonts] [-download-ms-fonts] [-font-dir dir] <input.docx> <output.pdf>\n", os.Args[0])
		os.Exit(2)
	}
	in, out := flag.Arg(0), flag.Arg(1)

	doc, err := document.Open(in)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = doc.Close() }()

	conv := document.ConvertToPdf(doc)
	if *fontDir != "" {
		conv.WithFontDir(*fontDir)
	}
	if *downloadFonts || *msFonts {
		dir, err := fonts.DefaultDir()
		if err != nil {
			log.Fatal(err)
		}
		if *msFonts {
			if err := fonts.DownloadMicrosoftFonts(context.Background(), dir); err != nil {
				log.Printf("some Microsoft fonts were not downloaded, their replacements are used: %v", err)
			}
		}
		if *downloadFonts {
			if err := fonts.Download(context.Background(), dir); err != nil {
				log.Printf("some fonts were not downloaded, their replacements are used: %v", err)
			}
		}
		conv.WithFontDir(dir)
	}
	if *systemFonts {
		conv.WithSystemFonts()
	}
	if err := conv.WriteToFile(out); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s\n", out)
}
