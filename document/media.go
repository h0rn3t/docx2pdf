package document

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"  // registers GIF for DecodeConfig (format sniffing, intrinsic size)
	_ "image/jpeg" // registers JPEG for DecodeConfig
	"image/png"
	"io"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	_ "golang.org/x/image/bmp"  // BMP, TIFF and WebP are decoded and re-encoded as PNG
	_ "golang.org/x/image/tiff" // for the backend, which embeds only PNG, JPEG and GIF
	_ "golang.org/x/image/webp"
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

// rawRelationships reads the relationship part belonging to partName (e.g.
// word/_rels/document.xml.rels for word/document.xml, or _rels/.rels for the
// package itself when partName is "") as declared. A missing or malformed .rels
// yields none.
func (p *pkg) rawRelationships(partName string) []xmlRelationship {
	relsName := "_rels/.rels"
	if partName != "" {
		relsName = path.Join(path.Dir(partName), "_rels", path.Base(partName)+".rels")
	}
	data, err := p.part(relsName)
	if err != nil {
		return nil
	}
	var xr xmlRelationships
	if xml.Unmarshal(data, &xr) != nil {
		return nil
	}
	return xr.Rels
}

// relationships is partName's relationships with each Target resolved to a part
// name: against the owning part's directory, or - for a Target starting with
// "/" - against the package root. External-mode relationships
// (TargetMode="External") are skipped: they point outside the package, and
// linked-not-embedded media is out of scope.
func (p *pkg) relationships(partName string) []xmlRelationship {
	dir := ""
	if partName != "" {
		dir = path.Dir(partName)
	}
	rels := p.rawRelationships(partName)
	out := rels[:0]
	for _, rel := range rels {
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
// VML imagedata) used inside one part into an Image carrying the media bytes,
// and a hyperlink's r:id into its URL. Each part gets its own resolver because
// ids are part-scoped: rId1 in word/numbering.xml and rId1 in word/document.xml
// are unrelated.
type imageResolver struct {
	pkg  *pkg
	rels map[string]string
	// links maps the part's external relationships (hyperlink targets) to their
	// URLs.
	links map[string]string
}

func (p *pkg) resolverFor(partName string) *imageResolver {
	links := make(map[string]string)
	for _, rel := range p.rawRelationships(partName) {
		if strings.EqualFold(rel.TargetMode, "External") && strings.HasSuffix(rel.Type, "/hyperlink") {
			links[rel.ID] = rel.Target
		}
	}
	return &imageResolver{pkg: p, rels: p.rels(partName), links: links}
}

// linkTarget is a hyperlink's Run.Link: its external URL (r:id) with the
// bookmark (w:anchor) as the URL's fragment, or "#anchor" alone for a link
// within the document; "" when it names neither. A URL whose scheme is not a
// plain web or mail one (javascript:, file:, ...) is dropped: the .docx is
// untrusted, and a PDF viewer would hand that URI to the system.
func (ir *imageResolver) linkTarget(rID, anchor string) string {
	var target string
	if ir != nil {
		target = ir.links[rID]
	}
	if target != "" {
		u, err := url.Parse(target)
		if err != nil || !linkSchemes[strings.ToLower(u.Scheme)] {
			target = ""
		}
	}
	if anchor == "" {
		return target
	}
	return target + "#" + anchor
}

// linkSchemes are the URL schemes a hyperlink may point at.
var linkSchemes = map[string]bool{"http": true, "https": true, "mailto": true, "ftp": true, "tel": true}

// resolve loads the image referenced by rID at the requested size, with the
// given color effects applied. A zero or missing size falls back to the
// picture's intrinsic pixel size at 96 dpi. It returns nil - and the caller
// drops the picture, keeping the conversion successful - when the id is
// unknown, the media part is missing, or its bytes are not an image that can be
// embedded (EMF/WMF/SVG, or a corrupt blob). The format is sniffed from the
// bytes, not the part's extension, so a mislabeled picture still draws instead
// of failing the whole PDF.
func (ir *imageResolver) resolve(rID string, widthPt, heightPt float64, fx imageEffects) *Image {
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
	if data != nil && fx != (imageEffects{}) {
		// The recolored picture is a picture of its own, cached and embedded
		// under its own name.
		name += "#" + fx.key()
		recolored, cached := ir.pkg.media[name]
		if !cached {
			recolored = applyEffects(data, fx)
			ir.pkg.media[name] = recolored
		}
		data = recolored
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

// maxDecodePixels caps the canvas of a picture decoded to be re-encoded: a
// decoded image takes 4 bytes or more per pixel, and an untrusted header can
// claim any size.
const maxDecodePixels = 50_000_000

// embeddable returns data in a form the fpdf backend accepts, or nil when it is
// not an image Go can decode. PNG, JPEG and GIF pass through; TIFF, BMP and
// WebP are re-encoded as PNG. fpdf rejects interlaced PNGs - and a rejected
// image fails the whole PDF - so those are re-encoded too, which Go's encoder
// always writes non-interlaced.
func embeddable(data []byte) []byte {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	// IHDR's interlace method is byte 28: 8 signature + 8 chunk header + 12.
	if fpdfImageTypes[format] != "" && (format != "png" || len(data) <= 28 || data[28] == 0) {
		return data
	}
	img := decodeBounded(data)
	if img == nil {
		return nil
	}
	return encodePNG(img)
}

// decodeBounded decodes data, or returns nil when it is corrupt or its canvas
// is larger than maxDecodePixels.
func decodeBounded(data []byte) image.Image {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return img
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// imageEffects are the recolorings a blip asks for (a:grayscl, a:lum,
// a:biLevel). The zero value changes nothing.
type imageEffects struct {
	gray bool
	// bright and contrast are a:lum's, as fractions in [-1, 1].
	bright, contrast float64
	// biLevel turns every pixel black or white around this luminance
	// threshold, in (0, 1]; zero means no a:biLevel.
	biLevel float64
}

func (fx imageEffects) key() string {
	return fmt.Sprintf("g%t-b%g-c%g-t%g", fx.gray, fx.bright, fx.contrast, fx.biLevel)
}

// applyEffects re-encodes the picture as a PNG with fx applied to every pixel,
// or returns nil when it cannot be decoded.
func applyEffects(data []byte, fx imageEffects) []byte {
	src := decodeBounded(data)
	if src == nil {
		return nil
	}
	// a:lum's contrast flattens toward mid-gray below zero and steepens above.
	scale := 1 + fx.contrast
	if fx.contrast > 0 {
		scale = 1 / max(1-fx.contrast, 0.01)
	}
	adjust := func(v float64) float64 {
		return min(max((v-0.5)*scale+0.5+fx.bright, 0), 1)
	}
	b := src.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			r, g, bl := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
			if fx.gray || fx.biLevel > 0 {
				lum := 0.299*r + 0.587*g + 0.114*bl
				if fx.biLevel > 0 {
					bw := 0.0
					if lum >= fx.biLevel {
						bw = 1
					}
					lum = bw
				}
				r, g, bl = lum, lum, lum
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(adjust(r)*255 + 0.5), G: uint8(adjust(g)*255 + 0.5), B: uint8(adjust(bl)*255 + 0.5), A: c.A,
			})
		}
	}
	return encodePNG(out)
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
	// Other holds the text boxes of the picture's other shapes (v:rect,
	// v:roundrect, v:group, …).
	Other []xmlTextBoxes `xml:",any"`
}

type xmlVShape struct {
	Style     string        `xml:"style,attr"`
	ImageData *xmlImageData `xml:"urn:schemas-microsoft-com:vml imagedata"`
	// TextBox holds the shape's v:textbox content.
	TextBox []xmlTextBoxes `xml:",any"`
}

// xmlTextBoxes collects the content of every text box (w:txbxContent) inside
// the element it decodes, at any depth: a DrawingML shape's wps:txbx, the
// shapes of a group, a VML shape's v:textbox.
type xmlTextBoxes struct {
	Blocks []xmlBodyChild
}

func (tb *xmlTextBoxes) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == wordNS && t.Name.Local == "txbxContent" {
				err = decodeBlocks(d, &tb.Blocks, skipAll)
			} else {
				err = tb.UnmarshalXML(d, t)
			}
			if err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		}
	}
}

// textBoxes is the content of the picture's text boxes, in document order.
func (p *xmlPict) textBoxes() []xmlBodyChild {
	var blocks []xmlBodyChild
	if p.Shape != nil {
		for _, tb := range p.Shape.TextBox {
			blocks = append(blocks, tb.Blocks...)
		}
	}
	for _, tb := range p.Other {
		blocks = append(blocks, tb.Blocks...)
	}
	return blocks
}

// shows reports whether the picture draws anything this package renders: an
// image or a text box.
func (p *xmlPict) shows() bool {
	return p.Shape != nil && p.Shape.ImageData != nil || len(p.textBoxes()) > 0
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
	return ir.resolve(p.Shape.ImageData.ID, cssLengthPt(style["width"]), cssLengthPt(style["height"]), imageEffects{})
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
// floating one (wp:anchor). Both shapes decode into the same struct; only a
// floating one carries a position and a wrap.
type xmlDrawing struct {
	Inline []xmlAnchor `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing inline"`
	Anchor []xmlAnchor `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing anchor"`
}

type xmlAnchor struct {
	Extent  xmlExtent  `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing extent"`
	Graphic xmlGraphic `xml:"http://schemas.openxmlformats.org/drawingml/2006/main graphic"`

	// The rest is wp:anchor's: the picture's position, either simplePos (from
	// the page's corner, when the simplePos attribute is on) or positionH/V; the
	// clearance the text keeps (EMU); and the wrap.
	SimplePosOn      string       `xml:"simplePos,attr"`
	DistT            string       `xml:"distT,attr"`
	DistB            string       `xml:"distB,attr"`
	DistL            string       `xml:"distL,attr"`
	DistR            string       `xml:"distR,attr"`
	SimplePos        *xmlPoint    `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing simplePos"`
	PositionH        *xmlPosition `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing positionH"`
	PositionV        *xmlPosition `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing positionV"`
	WrapNone         *struct{}    `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing wrapNone"`
	WrapTopAndBottom *struct{}    `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing wrapTopAndBottom"`
}

// xmlPoint is wp:simplePos's x/y, in EMU.
type xmlPoint struct {
	X string `xml:"x,attr"`
	Y string `xml:"y,attr"`
}

// xmlPosition is wp:positionH/wp:positionV: what the position is measured from,
// and either an alignment against it or an offset (EMU) from it.
type xmlPosition struct {
	RelativeFrom string  `xml:"relativeFrom,attr"`
	Align        *string `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing align"`
	PosOffset    *string `xml:"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing posOffset"`
}

// place resolves one axis of a wp:anchor: its anchor, and its alignment or
// offset in points.
func (p *xmlPosition) place() (Anchor, string, float64) {
	if p == nil {
		return AnchorText, "", 0
	}
	a := anchorFrom(p.RelativeFrom)
	if p.Align != nil {
		return a, strings.ToLower(strings.TrimSpace(*p.Align)), 0
	}
	if p.PosOffset != nil {
		return a, "", emuToPt(*p.PosOffset)
	}
	return a, "", 0
}

// float resolves a wp:anchor's placement and wrap.
func (a xmlAnchor) float() *Float {
	fl := &Float{
		DistLeftPt:   max(emuToPt(a.DistL), 0),
		DistRightPt:  max(emuToPt(a.DistR), 0),
		DistTopPt:    max(emuToPt(a.DistT), 0),
		DistBottomPt: max(emuToPt(a.DistB), 0),
	}
	switch {
	case a.WrapNone != nil:
		fl.Wrap = WrapNone
	case a.WrapTopAndBottom != nil:
		fl.Wrap = WrapTopAndBottom
	}
	switch strings.ToLower(strings.TrimSpace(a.SimplePosOn)) {
	case "1", "true", "on":
		if a.SimplePos != nil {
			fl.HorzAnchor, fl.VertAnchor = AnchorPage, AnchorPage
			fl.XPt, fl.YPt = emuToPt(a.SimplePos.X), emuToPt(a.SimplePos.Y)
			return fl
		}
	}
	fl.HorzAnchor, fl.XSpec, fl.XPt = a.PositionH.place()
	fl.VertAnchor, fl.YSpec, fl.YPt = a.PositionV.place()
	return fl
}

// xmlExtent is <wp:extent>'s picture size, in EMU (unqualified attributes).
type xmlExtent struct {
	Cx string `xml:"cx,attr"`
	Cy string `xml:"cy,attr"`
}

type xmlGraphic struct {
	Data xmlGraphicData `xml:"http://schemas.openxmlformats.org/drawingml/2006/main graphicData"`
}

// xmlGraphicData holds the graphic's payload: a picture (pic:pic), or the text
// boxes of a shape or group of shapes. Charts and diagrams decode to neither and
// are skipped.
type xmlGraphicData struct {
	Pic    *xmlPic        `xml:"http://schemas.openxmlformats.org/drawingml/2006/picture pic"`
	Shapes []xmlTextBoxes `xml:",any"`
}

type xmlPic struct {
	BlipFill xmlBlipFill `xml:"http://schemas.openxmlformats.org/drawingml/2006/picture blipFill"`
}

type xmlBlipFill struct {
	Blip    *xmlBlip    `xml:"http://schemas.openxmlformats.org/drawingml/2006/main blip"`
	SrcRect *xmlSrcRect `xml:"http://schemas.openxmlformats.org/drawingml/2006/main srcRect"`
}

// xmlSrcRect is <a:srcRect>: how much is cropped from each side, in thousandths
// of a percent of the picture's size.
type xmlSrcRect struct {
	L string `xml:"l,attr"`
	T string `xml:"t,attr"`
	R string `xml:"r,attr"`
	B string `xml:"b,attr"`
}

func (s *xmlSrcRect) crop() Crop {
	if s == nil {
		return Crop{}
	}
	frac := func(v string) float64 {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return f / 100000
	}
	return Crop{Left: frac(s.L), Top: frac(s.T), Right: frac(s.R), Bottom: frac(s.B)}
}

// xmlBlip is <a:blip>, whose r:embed names the media relationship, with the
// color effects it applies to the picture.
type xmlBlip struct {
	Embed   string      `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships embed,attr"`
	Grayscl *struct{}   `xml:"http://schemas.openxmlformats.org/drawingml/2006/main grayscl"`
	Lum     *xmlLum     `xml:"http://schemas.openxmlformats.org/drawingml/2006/main lum"`
	BiLevel *xmlBiLevel `xml:"http://schemas.openxmlformats.org/drawingml/2006/main biLevel"`
}

// xmlLum is <a:lum>: brightness and contrast, in thousandths of a percent.
type xmlLum struct {
	Bright   string `xml:"bright,attr"`
	Contrast string `xml:"contrast,attr"`
}

// xmlBiLevel is <a:biLevel>: its luminance threshold, in thousandths of a
// percent.
type xmlBiLevel struct {
	Thresh string `xml:"thresh,attr"`
}

func (b *xmlBlip) effects() imageEffects {
	frac := func(v string) float64 {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return min(max(f/100000, -1), 1)
	}
	fx := imageEffects{gray: b.Grayscl != nil}
	if b.Lum != nil {
		fx.bright, fx.contrast = frac(b.Lum.Bright), frac(b.Lum.Contrast)
	}
	if b.BiLevel != nil {
		fx.biLevel = 0.5
		if b.BiLevel.Thresh != "" {
			fx.biLevel = max(frac(b.BiLevel.Thresh), 0.001)
		}
	}
	return fx
}

// image resolves a w:drawing into an Image at the size its wp:extent declares,
// cropped and recolored as its blip says, or nil when it carries no embedded
// picture (a chart, a shape, a linked image). A floating picture (wp:anchor)
// comes with its placement.
func (d *xmlDrawing) image(ir *imageResolver) *Image {
	if d == nil {
		return nil
	}
	for _, a := range d.Inline {
		if img := a.picture(ir); img != nil {
			return img
		}
	}
	for _, a := range d.Anchor {
		if img := a.picture(ir); img != nil {
			img.Float = a.float()
			return img
		}
	}
	return nil
}

// textBoxes is the content of the drawing's text boxes, in document order.
func (d *xmlDrawing) textBoxes() []xmlBodyChild {
	var blocks []xmlBodyChild
	for _, a := range slices.Concat(d.Inline, d.Anchor) {
		for _, tb := range a.Graphic.Data.Shapes {
			blocks = append(blocks, tb.Blocks...)
		}
	}
	return blocks
}

// shows reports whether the drawing draws anything this package renders: a
// picture or a text box.
func (d *xmlDrawing) shows() bool {
	for _, a := range slices.Concat(d.Inline, d.Anchor) {
		if a.Graphic.Data.Pic != nil {
			return true
		}
	}
	return len(d.textBoxes()) > 0
}

func (a xmlAnchor) picture(ir *imageResolver) *Image {
	pic := a.Graphic.Data.Pic
	if pic == nil || pic.BlipFill.Blip == nil {
		return nil
	}
	img := ir.resolve(pic.BlipFill.Blip.Embed, emuToPt(a.Extent.Cx), emuToPt(a.Extent.Cy), pic.BlipFill.Blip.effects())
	if img != nil {
		img.Crop = pic.BlipFill.SrcRect.crop()
	}
	return img
}
