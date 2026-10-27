package document

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"  // registers GIF for DecodeConfig (format sniffing, intrinsic size)
	_ "image/jpeg" // registers JPEG for DecodeConfig
	"image/png"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// --- Package parts, relationships (word/_rels/*.rels) and media ---

type xmlRelationships struct {
	Rels []xmlRelationship `xml:"http://schemas.openxmlformats.org/package/2006/relationships Relationship"`
}

// xmlRelationship is one <Relationship> (its attributes are unqualified).
type xmlRelationship struct {
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

// pkg is the opened .docx: every part by name, plus a cache of already-read
// media blobs so an image used twice is read (and later embedded) once.
type pkg struct {
	parts map[string]*zip.File
	media map[string][]byte
}

func newPkg(files []*zip.File) *pkg {
	parts := make(map[string]*zip.File, len(files))
	for _, f := range files {
		parts[f.Name] = f
	}
	return &pkg{parts: parts, media: make(map[string][]byte)}
}

// maxPartSize caps how many decompressed bytes one package part may hold. A
// .docx is untrusted input and ZIP deflate reaches ratios above 1000:1, so
// without a cap a few kilobytes of archive could exhaust memory.
const maxPartSize = 256 << 20

// part returns the named part's decompressed bytes. The error wraps
// fs.ErrNotExist when the part is absent; any error means the part is unusable,
// and callers of optional parts degrade rather than failing Open.
func (p *pkg) part(name string) ([]byte, error) {
	f, ok := p.parts[name]
	if !ok {
		return nil, fmt.Errorf("part %s: %w", name, fs.ErrNotExist)
	}
	if f.UncompressedSize64 > maxPartSize {
		return nil, fmt.Errorf("part %s: larger than %d bytes", name, maxPartSize)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("part %s: %w", name, err)
	}
	defer func() { _ = rc.Close() }()

	// The declared size can lie, so the read itself is bounded too.
	b, err := io.ReadAll(io.LimitReader(rc, maxPartSize+1))
	if err != nil {
		return nil, fmt.Errorf("part %s: %w", name, err)
	}
	if len(b) > maxPartSize {
		return nil, fmt.Errorf("part %s: larger than %d bytes", name, maxPartSize)
	}
	return b, nil
}

// relationships reads the relationship part belonging to partName (e.g.
// word/_rels/document.xml.rels for word/document.xml, or _rels/.rels for the
// package itself when partName is "") with each Target resolved to a part name:
// against the owning part's directory, or - for a Target starting with "/" -
// against the package root. External-mode relationships (TargetMode="External")
// are skipped: they point outside the package, and linked-not-embedded media is
// out of scope. A missing or malformed .rels yields none.
func (p *pkg) relationships(partName string) []xmlRelationship {
	dir, relsName := "", "_rels/.rels"
	if partName != "" {
		dir = path.Dir(partName)
		relsName = path.Join(dir, "_rels", path.Base(partName)+".rels")
	}
	data, err := p.part(relsName)
	if err != nil {
		return nil
	}
	var xr xmlRelationships
	if xml.Unmarshal(data, &xr) != nil {
		return nil
	}
	out := xr.Rels[:0]
	for _, rel := range xr.Rels {
		if rel.ID == "" || rel.Target == "" || strings.EqualFold(rel.TargetMode, "External") {
			continue
		}
		if abs, ok := strings.CutPrefix(rel.Target, "/"); ok {
			rel.Target = path.Clean(abs)
		} else {
			rel.Target = path.Clean(path.Join(dir, rel.Target))
		}
		out = append(out, rel)
	}
	return out
}

// rels returns partName's relationships as rId → part name.
func (p *pkg) rels(partName string) map[string]string {
	rels := p.relationships(partName)
	out := make(map[string]string, len(rels))
	for _, rel := range rels {
		out[rel.ID] = rel.Target
	}
	return out
}

// relatedPart returns the part partName relates to by a relationship of the
// given kind (the last segment of its Type URI, e.g. "styles", so the
// transitional and strict OOXML namespaces both match), or fallback when it
// declares none.
func (p *pkg) relatedPart(partName, kind, fallback string) string {
	for _, rel := range p.relationships(partName) {
		if strings.HasSuffix(rel.Type, "/"+kind) {
			return rel.Target
		}
	}
	return fallback
}

// imageResolver turns a relationship id (r:embed on a DrawingML blip, r:id on a
// VML imagedata) used inside one part into an Image carrying the media bytes.
// Each part gets its own resolver because ids are part-scoped: rId1 in
// word/numbering.xml and rId1 in word/document.xml are unrelated.
type imageResolver struct {
	pkg  *pkg
	rels map[string]string
}

func (p *pkg) resolverFor(partName string) *imageResolver {
	return &imageResolver{pkg: p, rels: p.rels(partName)}
}

// resolve loads the image referenced by rID at the requested size. A zero or
// missing size falls back to the picture's intrinsic pixel size at 96 dpi.
// It returns nil - and the caller drops the picture, keeping the conversion
// successful - when the id is unknown, the media part is missing, or its bytes
// are not an image the PDF backend can embed (EMF/WMF/SVG/TIFF, or a corrupt
// blob). The format is sniffed from the bytes, not the part's extension, so a
// mislabeled picture still draws instead of failing the whole PDF.
func (ir *imageResolver) resolve(rID string, widthPt, heightPt float64) *Image {
	if ir == nil || rID == "" {
		return nil
	}
	name := ir.rels[rID]
	if name == "" {
		return nil
	}
	data, cached := ir.pkg.media[name]
	if !cached {
		if raw, err := ir.pkg.part(name); err == nil {
			data = embeddable(raw)
		}
		ir.pkg.media[name] = data // nil remembers an unusable part
	}
	if data == nil {
		return nil
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	if widthPt <= 0 || heightPt <= 0 {
		// Intrinsic size: pixels at 96 dpi, Word's own screen-pixel assumption.
		widthPt, heightPt = float64(cfg.Width)*72/96, float64(cfg.Height)*72/96
		if widthPt <= 0 || heightPt <= 0 {
			return nil
		}
	}
	return &Image{Name: name, Type: fpdfImageTypes[format], Data: data, WidthPt: widthPt, HeightPt: heightPt}
}

// fpdfImageTypes maps an image.DecodeConfig format name onto the fpdf image
// type; a format missing here is one the backend cannot embed.
var fpdfImageTypes = map[string]string{"png": "PNG", "jpeg": "JPG", "gif": "GIF"}

// embeddable returns data in a form the fpdf backend accepts, or nil when it is
// not a PNG, JPEG or GIF. fpdf rejects interlaced PNGs - and a rejected image
// fails the whole PDF - so those are re-encoded, which Go's encoder always
// writes non-interlaced.
func embeddable(data []byte) []byte {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || fpdfImageTypes[format] == "" {
		return nil
	}
	// IHDR's interlace method is byte 28: 8 signature + 8 chunk header + 12.
	if format != "png" || len(data) <= 28 || data[28] == 0 {
		return data
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// emuToPt converts English Metric Units (the unit wp:extent uses) to points:
// 914400 EMU per inch, 72 pt per inch.
func emuToPt(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v / 12700
}

// --- VML (w:pict): legacy pictures and picture bullets ---
//
// A picture bullet (w:numPicBullet) is always VML, even in files Word writes
// today, so the shape's size comes from its CSS-ish style attribute rather than
// a wp:extent.

type xmlPict struct {
	Shape *xmlVShape `xml:"urn:schemas-microsoft-com:vml shape"`
}

type xmlVShape struct {
	Style     string        `xml:"style,attr"`
	ImageData *xmlImageData `xml:"urn:schemas-microsoft-com:vml imagedata"`
}

type xmlImageData struct {
	ID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
}

// image resolves a VML shape into an Image sized from its style attribute
// (e.g. style="width:11.25pt;height:11.25pt"); an absent or unparseable size
// falls back to the picture's intrinsic size.
func (p *xmlPict) image(ir *imageResolver) *Image {
	if p == nil || p.Shape == nil || p.Shape.ImageData == nil {
		return nil
	}
	style := parseVMLStyle(p.Shape.Style)
	return ir.resolve(p.Shape.ImageData.ID, cssLengthPt(style["width"]), cssLengthPt(style["height"]))
}

// parseVMLStyle splits a VML style attribute ("width:9pt;height:9pt") into its
// lower-cased property→value pairs.
func parseVMLStyle(style string) map[string]string {
	out := make(map[string]string)
	for decl := range strings.SplitSeq(style, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.ToLower(strings.TrimSpace(v))
	}
	return out
}

// cssLengthPt converts a VML/CSS length ("11.25pt", "15px", "0.25in") to
// points; a unitless or unknown-unit value yields 0, so the caller falls back to
// the image's intrinsic size.
func cssLengthPt(v string) float64 {
	for unit, perUnit := range map[string]float64{
		"pt": 1,
		"px": 72.0 / 96,
		"in": 72,
		"pc": 12,
		"mm": 72 / 25.4,
		"cm": 72 / 2.54,
	} {
		if num, ok := strings.CutSuffix(v, unit); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil || f <= 0 {
				return 0
			}
			return f * perUnit
		}
	}
	return 0
}

// --- DrawingML (w:drawing): inline and floating pictures ---

// xmlDrawing is a run's <w:drawing>: an inline picture (wp:inline) or a
// floating one (wp:anchor). Floating pictures are laid out inline too - text
// wrapping and absolute positioning are out of scope - so both shapes decode
// into the same struct.
type xmlDrawing struct {
	Inline []xmlAnchor `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing inline"`
	Anchor []xmlAnchor `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing anchor"`
}

type xmlAnchor struct {
	Extent  xmlExtent  `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing extent"`
	Graphic xmlGraphic `xml:"http://schemas.openxmlformats.org/drawingml/2006/main graphic"`
}

// xmlExtent is <wp:extent>'s picture size, in EMU (unqualified attributes).
type xmlExtent struct {
	Cx string `xml:"cx,attr"`
	Cy string `xml:"cy,attr"`
}

type xmlGraphic struct {
	Data xmlGraphicData `xml:"http://schemas.openxmlformats.org/drawingml/2006/main graphicData"`
}

// xmlGraphicData holds the graphic's payload; only a picture (pic:pic) is
// modeled, so charts, diagrams and shapes decode to a nil Pic and are skipped.
type xmlGraphicData struct {
	Pic *xmlPic `xml:"http://schemas.openxmlformats.org/drawingml/2006/picture pic"`
}

type xmlPic struct {
	BlipFill xmlBlipFill `xml:"http://schemas.openxmlformats.org/drawingml/2006/picture blipFill"`
}

type xmlBlipFill struct {
	Blip *xmlBlip `xml:"http://schemas.openxmlformats.org/drawingml/2006/main blip"`
}

// xmlBlip is <a:blip>, whose r:embed names the media relationship.
type xmlBlip struct {
	Embed string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships embed,attr"`
}

// image resolves a w:drawing into an Image at the size its wp:extent declares,
// or nil when it carries no embedded picture (a chart, a shape, a linked image).
func (d *xmlDrawing) image(ir *imageResolver) *Image {
	if d == nil {
		return nil
	}
	for _, a := range append(append([]xmlAnchor{}, d.Inline...), d.Anchor...) {
		pic := a.Graphic.Data.Pic
		if pic == nil || pic.BlipFill.Blip == nil {
			continue
		}
		if img := ir.resolve(pic.BlipFill.Blip.Embed, emuToPt(a.Extent.Cx), emuToPt(a.Extent.Cy)); img != nil {
			return img
		}
	}
	return nil
}
