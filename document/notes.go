package document

import (
	"encoding/xml"
	"strings"
)

// --- Footnotes and endnotes (word/footnotes.xml, word/endnotes.xml) ---
//
// A note's content lives in its own part; the body only holds a reference
// (w:footnoteReference/w:endnoteReference) by id. Word numbers the notes in the
// order they are referenced, not by id, so a note is built - and numbered - when
// its reference is reached.

// xmlNotes is a footnotes or endnotes part: its w:footnote or w:endnote
// children.
type xmlNotes struct {
	Footnotes []xmlNote `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main footnote"`
	Endnotes  []xmlNote `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main endnote"`
}

// xmlNote is one note: its id, its type ("" for an ordinary note, or a
// separator Word draws between the text and the notes) and its content.
type xmlNote struct {
	ID   string
	Type string
	Body xmlBody
}

func (n *xmlNote) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Space != wordNS {
			continue
		}
		switch a.Name.Local {
		case "id":
			n.ID = a.Value
		case "type":
			n.Type = a.Value
		}
	}
	return n.Body.UnmarshalXML(d, start)
}

// xmlNoteRef is a w:footnoteReference or w:endnoteReference.
type xmlNoteRef struct {
	ID      string `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main id,attr"`
	endnote bool
}

// xmlNotePr is w:footnotePr/w:endnotePr in word/settings.xml: the notes'
// number format.
type xmlNotePr struct {
	NumFmt *xmlVal `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main numFmt"`
}

// noteSource is one notes part as the body is built: its notes by id, the
// resolver for the part's own relationships, the number format, and how many
// notes have been referenced so far.
type noteSource struct {
	notes   map[string]xmlBody
	images  *imageResolver
	numFmt  string
	endnote bool
	count   int
	// built is every note built so far, in reference order: the endnotes the
	// document prints at its end.
	built []BodyElement
}

// newNoteSource reads the notes part name (nil when it is absent or holds no
// note): footnotes, or endnotes when endnote is set, numbered in numFmt.
func newNoteSource(p *pkg, name, numFmt string, endnote bool) *noteSource {
	part := optionalPart[xmlNotes](p, name)
	if part == nil {
		return nil
	}
	notes := part.Footnotes
	if endnote {
		notes = part.Endnotes
	}
	ns := &noteSource{notes: make(map[string]xmlBody), images: p.resolverFor(name), numFmt: numFmt, endnote: endnote}
	for _, n := range notes {
		if n.Type == "" || strings.EqualFold(n.Type, "normal") {
			ns.notes[strings.TrimSpace(n.ID)] = n.Body
		}
	}
	if len(ns.notes) == 0 {
		return nil
	}
	return ns
}

// reference builds the note id refers to, numbered next, and returns it with
// its number; nil when the part has no such note. The note's content is built
// with the part's own relationships and list counters, and inside it its
// w:footnoteRef/w:endnoteRef mark shows that number.
func (ns *noteSource) reference(id string, sc styleContext) (*Note, string) {
	if ns == nil {
		return nil, ""
	}
	xb, ok := ns.notes[strings.TrimSpace(id)]
	if !ok {
		return nil, ""
	}
	ns.count++
	mark := formatNumber(ns.count, ns.numFmt)
	nsc := sc
	nsc.images = ns.images
	nsc.numbering = sc.numbering.fork()
	nsc.noteMark = mark
	nsc.footnotes, nsc.endnotes = nil, nil // a note cannot hold another
	body, _ := buildBody(xb, nsc)
	if ns.endnote {
		ns.built = append(ns.built, body...)
	}
	return &Note{Body: body, Endnote: ns.endnote}, mark
}

// noteMarkProps is a note number's formatting: its run's own (normally the
// FootnoteReference style, which is superscript), raised as superscript when
// that declares no vertical position.
func noteMarkProps(props RunProperties) RunProperties {
	if props.BaselineShiftPt == 0 {
		props.BaselineShiftPt = props.SizePt / 3
		props.SizePt *= superSubScale
	}
	return props
}
