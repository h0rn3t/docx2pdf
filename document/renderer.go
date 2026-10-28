package document

import (
	"io"
)

const (
	// lineSpacing multiplies a line's tallest font size to get its box height.
	lineSpacing = 1.2

	// minRowHeightPt floors a table row's height so an all-empty or
	// all-vMerge-continuation row still occupies visible space: one default line.
	minRowHeightPt = defaultRenderSizePt * lineSpacing
)

// page is the per-document layout frame in points, derived from the
// document's PageGeometry: where content starts (left/top margin), how wide it
// may be, and the y beyond which content must paginate (page height minus the
// bottom margin).
type page struct {
	originX, originY float64
	contentWidth     float64
	bottomLimit      float64
}

func pageFrom(g PageGeometry) page {
	return page{
		originX:      g.MarginLeftPt,
		originY:      g.MarginTopPt,
		contentWidth: g.WidthPt - g.MarginLeftPt - g.MarginRightPt,
		bottomLimit:  g.HeightPt - g.MarginBottomPt,
	}
}

// renderer is the thin seam over the PDF backend. Coordinates and sizes are in
// points; the origin is the top-left of the page and DrawText positions text by
// its baseline. Keeping this interface minimal lets the fpdf backend be swapped
// (or gain TTF embedding) without touching the layout or public API.
type renderer interface {
	// SetFont selects the active font for subsequent TextWidth/DrawText calls;
	// underline and strike decorate the text DrawText draws.
	SetFont(family string, bold, italic, underline, strike bool, sizePt float64)
	// SetTextColor sets the fill color for subsequent DrawText calls from a
	// "#RRGGBB" string; an empty or malformed value selects black.
	SetTextColor(colorHex string)
	// TextWidth measures s in the active font, in points.
	TextWidth(s string) float64
	// AddPage starts a new page of the given size and makes it current. Pages
	// are sized per section, so a document whose sections differ (say a
	// landscape one) gets pages of different sizes.
	AddPage(widthPt, heightPt float64)
	// DrawText draws s with its baseline at (x, y) in the active font.
	DrawText(x, y float64, s string)
	// DrawImage draws img in the rectangle with top-left corner (x, y). An
	// image is embedded once per Image.Name however often it is drawn.
	DrawImage(x, y, w, h float64, img *Image)
	// FillRect fills the rectangle with top-left corner (x, y), width w and
	// height h, in the given "#RRGGBB" color. A no-op for an empty colorHex.
	FillRect(x, y, w, h float64, colorHex string)
	// StrokeLine draws a line from (x1, y1) to (x2, y2) with the given width
	// (points) and "#RRGGBB" color. A no-op for an empty colorHex.
	StrokeLine(x1, y1, x2, y2, widthPt float64, colorHex string)
	// Rotate turns the coordinate system deg degrees counter-clockwise about
	// (x, y) for everything drawn until the matching RotateEnd - which is how a
	// cell's vertical text (w:textDirection) is drawn with the ordinary
	// horizontal layout code.
	Rotate(deg, x, y float64)
	// RotateEnd undoes the last Rotate.
	RotateEnd()
	// Clip confines everything drawn until the matching ClipEnd to the given
	// rectangle - how a cell of a fixed-height row (w:hRule="exact") keeps its
	// overflowing content to itself.
	Clip(x, y, w, h float64)
	// ClipEnd undoes the last Clip.
	ClipEnd()
	// Link makes the rectangle a clickable link to target: an external URL, or
	// "#name" for the place Anchor marked with that name (see Run.Link).
	Link(x, y, w, h float64, target string)
	// Anchor marks y on the current page as the destination of "#name" links.
	Anchor(name string, y float64)
	// Output writes the finished PDF, returning any accumulated backend error.
	Output(w io.Writer) error
}
