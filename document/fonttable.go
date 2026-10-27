package document

import (
	"encoding/hex"
	"strings"

	"golang.org/x/image/font/sfnt"
)

// --- Fonts embedded in the package (word/fontTable.xml) ---
//
// Word can save the fonts a document uses inside it (File > Options > Save >
// Embed fonts). Each face is a part of its own, obfuscated: the first 32 bytes
// are XORed with a key taken from the GUID in its w:fontKey.

// xmlFontTable is word/fontTable.xml: the fonts the document uses, with the
// parts holding any it embeds.
type xmlFontTable struct {
	Fonts []xmlFont `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main font"`
}

type xmlFont struct {
	Name       string        `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main name,attr"`
	Regular    *xmlFontEmbed `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main embedRegular"`
	Bold       *xmlFontEmbed `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main embedBold"`
	Italic     *xmlFontEmbed `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main embedItalic"`
	BoldItalic *xmlFontEmbed `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main embedBoldItalic"`
}

// xmlFontEmbed is one embedded face: its part (r:id) and obfuscation key.
type xmlFontEmbed struct {
	ID      string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	FontKey string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main fontKey,attr"`
}

// embeddedFonts reads the faces the font table part name embeds. A face whose
// part is missing, whose key is malformed, or whose bytes are not a font is
// skipped: its text then falls back to a replacement family.
func embeddedFonts(p *pkg, name string) []Font {
	table := optionalPart[xmlFontTable](p, name)
	if table == nil {
		return nil
	}
	rels := p.rels(name)
	var out []Font
	for _, f := range table.Fonts {
		for _, face := range []struct {
			embed        *xmlFontEmbed
			bold, italic bool
		}{{f.Regular, false, false}, {f.Bold, true, false}, {f.Italic, false, true}, {f.BoldItalic, true, true}} {
			if face.embed == nil || f.Name == "" {
				continue
			}
			data, err := p.part(rels[face.embed.ID])
			if err != nil || !deobfuscateFont(data, face.embed.FontKey) {
				continue
			}
			if _, err := sfnt.Parse(data); err != nil {
				continue
			}
			out = append(out, Font{Family: f.Name, Bold: face.bold, Italic: face.italic, Data: data})
		}
	}
	return out
}

// deobfuscateFont undoes an embedded font's obfuscation in place: its first 32
// bytes XORed with the 16 bytes of the GUID fontKey, taken in reverse order.
// It reports false when the key is not a GUID or the data too short.
func deobfuscateFont(data []byte, fontKey string) bool {
	digits := strings.NewReplacer("{", "", "}", "", "-", "").Replace(strings.TrimSpace(fontKey))
	guid, err := hex.DecodeString(digits)
	if err != nil || len(guid) != 16 || len(data) < 32 {
		return false
	}
	for i := range 32 {
		data[i] ^= guid[15-i%16]
	}
	return true
}
