package pdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sort"
	"strings"

	// JPEG is registered here so we can image.Decode panel bytes that came
	// from DCTDecode passthrough.
	_ "image/jpeg"
)

var rgbaWhite = color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}

// FigureGroup is a set of Images that the page layout indicates belong
// together. Single-panel groups represent standalone images; multi-panel
// groups represent figures that the PDF split into adjacent panels.
//
// Parts is in display order: top-to-bottom for Layout=="vertical",
// left-to-right for Layout=="horizontal". For single-panel groups, Layout
// is empty.
type FigureGroup struct {
	Page   int
	Layout string
	Parts  []Image
}

// GroupAdjacent partitions images by page and clusters adjacent panels into
// FigureGroups. Two images are adjacent when they share a page, their bbox
// extents on one axis match within tolPt, and their bbox edges on the
// orthogonal axis abut within tolPt.
//
// Images with a zero-area bbox (typically: defined in Resources but never
// painted, so the CTM walker couldn't recover a position) pass through as
// single-panel groups.
func GroupAdjacent(images []Image, tolPt float64) []FigureGroup {
	if len(images) == 0 {
		return nil
	}

	byPage := map[int][]int{}
	for i, img := range images {
		byPage[img.Page] = append(byPage[img.Page], i)
	}

	pages := make([]int, 0, len(byPage))
	for p := range byPage {
		pages = append(pages, p)
	}
	sort.Ints(pages)

	var groups []FigureGroup
	for _, page := range pages {
		idxs := byPage[page]
		groups = append(groups, groupPage(images, idxs, tolPt)...)
	}
	return groups
}

// groupPage runs adjacency clustering for one page. n images is small in
// practice (rarely more than 5), so an O(n²) connectivity scan is fine.
func groupPage(all []Image, idxs []int, tolPt float64) []FigureGroup {
	n := len(idxs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(i, j int) {
		ri, rj := find(i), find(j)
		if ri != rj {
			parent[ri] = rj
		}
	}

	// adjacencyLayout returns "vertical"/"horizontal"/"" for the two panels.
	for i := range n {
		for j := i + 1; j < n; j++ {
			a := all[idxs[i]]
			b := all[idxs[j]]
			if a.BboxW == 0 || b.BboxW == 0 {
				continue
			}
			if adjacencyLayout(a, b, tolPt) != "" {
				union(i, j)
			}
		}
	}

	// Collect components.
	components := map[int][]int{}
	for i := range n {
		r := find(i)
		components[r] = append(components[r], idxs[i])
	}

	// Sort component roots for stable output order: by min stream-index
	// (which approximates display order).
	roots := make([]int, 0, len(components))
	for r := range components {
		roots = append(roots, r)
	}
	sort.Slice(roots, func(a, b int) bool {
		return components[roots[a]][0] < components[roots[b]][0]
	})

	page := all[idxs[0]].Page
	var out []FigureGroup
	for _, r := range roots {
		members := components[r]
		parts := make([]Image, len(members))
		for k, m := range members {
			parts[k] = all[m]
		}
		layout := ""
		if len(parts) > 1 {
			layout = detectLayout(parts, tolPt)
			sortPartsForLayout(parts, layout)
		}
		out = append(out, FigureGroup{Page: page, Layout: layout, Parts: parts})
	}
	return out
}

// adjacencyLayout returns "vertical" if a and b share an x-extent and their
// y-edges abut; "horizontal" if they share a y-extent and their x-edges
// abut; "" otherwise.
func adjacencyLayout(a, b Image, tol float64) string {
	xSame := absFloat(a.BboxX-b.BboxX) <= tol &&
		absFloat((a.BboxX+a.BboxW)-(b.BboxX+b.BboxW)) <= tol
	ySame := absFloat(a.BboxY-b.BboxY) <= tol &&
		absFloat((a.BboxY+a.BboxH)-(b.BboxY+b.BboxH)) <= tol

	yAbut := absFloat(a.BboxY-(b.BboxY+b.BboxH)) <= tol ||
		absFloat(b.BboxY-(a.BboxY+a.BboxH)) <= tol
	xAbut := absFloat(a.BboxX-(b.BboxX+b.BboxW)) <= tol ||
		absFloat(b.BboxX-(a.BboxX+a.BboxW)) <= tol

	switch {
	case xSame && yAbut:
		return "vertical"
	case ySame && xAbut:
		return "horizontal"
	default:
		return ""
	}
}

// detectLayout picks the dominant axis of a multi-panel group by majority
// pairwise adjacency. In practice all panels in our corpus are uniform.
func detectLayout(parts []Image, tol float64) string {
	vCount, hCount := 0, 0
	for i := range parts {
		for j := i + 1; j < len(parts); j++ {
			switch adjacencyLayout(parts[i], parts[j], tol) {
			case "vertical":
				vCount++
			case "horizontal":
				hCount++
			}
		}
	}
	if hCount > vCount {
		return "horizontal"
	}
	return "vertical"
}

// sortPartsForLayout reorders parts to match display order.
func sortPartsForLayout(parts []Image, layout string) {
	switch layout {
	case "vertical":
		// PDF Y grows upward; topmost panel has the largest Y.
		sort.SliceStable(parts, func(i, j int) bool {
			return parts[i].BboxY > parts[j].BboxY
		})
	case "horizontal":
		sort.SliceStable(parts, func(i, j int) bool {
			return parts[i].BboxX < parts[j].BboxX
		})
	}
}

// StitchGroup composites a multi-panel FigureGroup into a single PNG Image.
// Panels are stacked in their display-order positions in the canvas, with
// pixel offsets derived from each panel's pixel size (assumes uniform DPI,
// which holds for the doctrinal PDFs in our corpus where all panels of a
// figure share dimensions per axis).
//
// Single-panel groups are returned unchanged.
func StitchGroup(g FigureGroup) (Image, error) {
	if len(g.Parts) == 0 {
		return Image{}, fmt.Errorf("empty figure group on page %d", g.Page)
	}
	if len(g.Parts) == 1 {
		return g.Parts[0], nil
	}

	panels := make([]image.Image, len(g.Parts))
	for i, p := range g.Parts {
		img, _, err := image.Decode(bytes.NewReader(p.Data))
		if err != nil {
			return Image{}, fmt.Errorf("decode panel %d (%s): %w", i, p.Name, err)
		}
		panels[i] = img
	}

	var (
		canvasW, canvasH int
		positions        []image.Point
	)
	switch g.Layout {
	case "vertical":
		maxW := 0
		totalH := 0
		positions = make([]image.Point, len(panels))
		for i, im := range panels {
			b := im.Bounds()
			positions[i] = image.Point{X: 0, Y: totalH}
			totalH += b.Dy()
			if b.Dx() > maxW {
				maxW = b.Dx()
			}
		}
		canvasW, canvasH = maxW, totalH

	case "horizontal":
		maxH := 0
		totalW := 0
		positions = make([]image.Point, len(panels))
		for i, im := range panels {
			b := im.Bounds()
			positions[i] = image.Point{X: totalW, Y: 0}
			totalW += b.Dx()
			if b.Dy() > maxH {
				maxH = b.Dy()
			}
		}
		canvasW, canvasH = totalW, maxH

	default:
		return Image{}, fmt.Errorf("unknown layout %q for group on page %d", g.Layout, g.Page)
	}

	canvas := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	// Paint a white background so panels with transparency don't leak through.
	white := image.NewUniform(rgbaWhite)
	draw.Draw(canvas, canvas.Bounds(), white, image.Point{}, draw.Src)

	for i, im := range panels {
		offset := positions[i]
		rect := image.Rect(offset.X, offset.Y, offset.X+im.Bounds().Dx(), offset.Y+im.Bounds().Dy())
		draw.Draw(canvas, rect, im, im.Bounds().Min, draw.Over)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return Image{}, fmt.Errorf("png encode: %w", err)
	}

	first := g.Parts[0]
	stitched := Image{
		Page:             first.Page,
		Name:             stitchedName(g.Parts),
		Width:            canvasW,
		Height:           canvasH,
		BitsPerComponent: 8,
		ColorSpace:       "stitched",
		Filter:           "stitched",
		Ext:              "png",
		Data:             buf.Bytes(),
		BboxX:            unionBboxX(g.Parts),
		BboxY:            unionBboxY(g.Parts),
		BboxW:            unionBboxW(g.Parts),
		BboxH:            unionBboxH(g.Parts),
	}
	return stitched, nil
}

// stitchedName joins panel names with a "+" so the manifest still traces back
// to the source XObjects (e.g. "Im0+Im1+Im2").
func stitchedName(parts []Image) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(parts[0].Name)
	for _, p := range parts[1:] {
		b.WriteByte('+')
		b.WriteString(p.Name)
	}
	return b.String()
}

func unionBboxX(parts []Image) float64 {
	min := parts[0].BboxX
	for _, p := range parts[1:] {
		if p.BboxX < min {
			min = p.BboxX
		}
	}
	return min
}

func unionBboxY(parts []Image) float64 {
	min := parts[0].BboxY
	for _, p := range parts[1:] {
		if p.BboxY < min {
			min = p.BboxY
		}
	}
	return min
}

func unionBboxW(parts []Image) float64 {
	maxRight := parts[0].BboxX + parts[0].BboxW
	min := parts[0].BboxX
	for _, p := range parts[1:] {
		if right := p.BboxX + p.BboxW; right > maxRight {
			maxRight = right
		}
		if p.BboxX < min {
			min = p.BboxX
		}
	}
	return maxRight - min
}

func unionBboxH(parts []Image) float64 {
	maxTop := parts[0].BboxY + parts[0].BboxH
	min := parts[0].BboxY
	for _, p := range parts[1:] {
		if top := p.BboxY + p.BboxH; top > maxTop {
			maxTop = top
		}
		if p.BboxY < min {
			min = p.BboxY
		}
	}
	return maxTop - min
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
