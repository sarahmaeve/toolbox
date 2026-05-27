package pdf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"log"
	"slices"
)

// maxImagePixels caps how many pixels an image extracted from a PDF may
// claim. A hostile document declaring Width × Height in the billions would
// otherwise drive image.NewRGBA/NewGray into a multi-TB allocation —
// pixelBufferLength's overflow guard panics with a non-recoverable runtime
// error, killing the host. 100 megapixels is comfortably above any real
// figure (a 10000×10000 photo) while bounding the worst case.
const maxImagePixels = 100 * 1000 * 1000

// Image holds an extracted Image XObject ready to write to disk. Data is
// already in the encoding implied by Ext.
//
// BboxX/Y/W/H give the image's painted location on the page in PDF points
// (lower-left origin), recovered from the page content stream's CTM at the
// time of the corresponding "Do" invocation. They are zero when the image
// is defined in the page's Resources but never actually painted, and
// otherwise reflect the first placement encountered in stream order.
type Image struct {
	Page             int    // 1-indexed page number
	Name             string // XObject resource name (e.g. "Im0")
	Width            int    // pixels
	Height           int    // pixels
	BitsPerComponent int
	ColorSpace       string // "DeviceGray", "DeviceRGB", "DeviceCMYK", "Indexed", ...
	Filter           string // outer filter used to derive Data (e.g. "DCTDecode")
	Ext              string // recommended file extension: "jpg", "png", "tif", "jp2"
	Data             []byte // ready-to-write bytes (matching Ext)

	BboxX, BboxY float64 // lower-left corner in PDF points
	BboxW, BboxH float64 // dimensions in PDF points
}

// extractPageImages walks the page's Resources["XObject"] dictionary and
// returns every entry whose /Subtype is /Image, in lexical resource-name order
// so output is deterministic across runs.
//
// Form XObjects (the other common /Subtype) are skipped — they can themselves
// contain image XObjects, but those are reached transitively when the form is
// rendered; resolving them here would double-count.
func (f *pdfFile) extractPageImages(ref pdfRef, pageNum int) ([]Image, error) {
	page := f.getDict(f.resolve(ref))
	if page == nil {
		return nil, fmt.Errorf("page object %d is not a dict", ref.num)
	}

	resources := f.getDict(page["Resources"])
	if resources == nil {
		return nil, nil
	}
	xobjects := f.getDict(resources["XObject"])
	if xobjects == nil {
		return nil, nil
	}

	// Walk the page content stream to recover where each XObject was painted.
	// Failures here are non-fatal: we still want to emit the images, just
	// without bbox data. A missing Contents entry is a normal case (XObjects
	// defined but never invoked) and not worth a warning.
	bboxByName := map[string]bbox{}
	if content, err := f.getPageContent(page); err == nil && len(content) > 0 {
		for _, p := range walkXObjectPlacements(content) {
			if _, seen := bboxByName[p.name]; !seen {
				bboxByName[p.name] = p.box
			}
		}
	}

	names := make([]string, 0, len(xobjects))
	for n := range xobjects {
		names = append(names, n)
	}
	slices.Sort(names)

	var out []Image
	for _, name := range names {
		stream, ok := f.getStream(xobjects[name])
		if !ok {
			continue
		}
		if f.getName(stream.dict["Subtype"]) != "Image" {
			continue
		}
		img, reason, err := f.imageFromStream(*stream, pageNum, name)
		if err != nil {
			log.Printf("warning: page %d image %q: %v", pageNum, name, err)
			continue
		}
		if reason != "" {
			log.Printf("warning: page %d image %q: %s, skipping", pageNum, name, reason)
			continue
		}
		if box, found := bboxByName[name]; found {
			img.BboxX, img.BboxY, img.BboxW, img.BboxH = box.X, box.Y, box.W, box.H
		}
		out = append(out, img)
	}
	return out, nil
}

// imageFromStream converts a /Subtype /Image stream into a writable Image.
// Returns (img, "", nil) on success. Returns (Image{}, reason, nil) when the
// image cannot be written in its current form — reason is a short caller-
// friendly string for logging (e.g. "indexed colorspace not yet supported").
// Returns a non-nil error only on hard decode failures.
func (f *pdfFile) imageFromStream(s pdfStream, page int, name string) (Image, string, error) {
	cs := f.resolveImageColorSpace(s.dict["ColorSpace"])

	img := Image{
		Page:             page,
		Name:             name,
		Width:            f.getInt(s.dict["Width"], 0),
		Height:           f.getInt(s.dict["Height"], 0),
		BitsPerComponent: f.getInt(s.dict["BitsPerComponent"], 8),
		ColorSpace:       cs.label,
	}

	outer := outerFilter(f, s.dict["Filter"])
	img.Filter = outer

	switch outer {
	case "DCTDecode":
		img.Ext = "jpg"
		img.Data = s.data
		return img, "", nil

	case "JPXDecode":
		img.Ext = "jp2"
		img.Data = s.data
		return img, "", nil

	case "CCITTFaxDecode":
		parms := f.getDict(s.dict["DecodeParms"])
		data, err := wrapCCITTAsTIFF(s.data, img.Width, img.Height, parms)
		if err != nil {
			return Image{}, "", fmt.Errorf("ccitt tiff wrap: %w", err)
		}
		img.Ext = "tif"
		img.Data = data
		return img, "", nil

	case "FlateDecode", "LZWDecode", "":
		decoded, err := f.decodeStream(s)
		if err != nil {
			return Image{}, "", fmt.Errorf("decode raster: %w", err)
		}
		pngBytes, reason, err := encodeRasterAsPNG(decoded, img.Width, img.Height, img.BitsPerComponent, cs)
		if err != nil {
			return Image{}, "", fmt.Errorf("png encode: %w", err)
		}
		if reason != "" {
			return Image{}, reason, nil
		}
		img.Ext = "png"
		img.Data = pngBytes
		return img, "", nil

	default:
		return Image{}, fmt.Sprintf("unsupported filter %q", outer), nil
	}
}

// outerFilter returns the last filter name in a /Filter chain, which is the
// one that defines the final byte format. Bare /Filter /DCTDecode and
// /Filter [/ASCII85Decode /DCTDecode] both yield "DCTDecode".
func outerFilter(f *pdfFile, v any) string {
	resolved := f.resolve(v)
	switch fv := resolved.(type) {
	case pdfName:
		return string(fv)
	case pdfArray:
		if len(fv) == 0 {
			return ""
		}
		return f.getName(fv[len(fv)-1])
	}
	return ""
}

// effectiveColorSpace is the resolved form of an image's /ColorSpace entry.
// kind drives the encoder; label is a human-friendly tag for the manifest.
//
// For kind == "indexed_rgb", paletteRGB holds (hival+1)*3 bytes — the original
// /Lookup palette is already projected into RGB so the encoder can simply
// index it.
type effectiveColorSpace struct {
	kind       string // "gray", "rgb", "cmyk", "indexed_rgb", or "" for unsupported
	label      string // "DeviceRGB", "ICCBased (N=3)", "Indexed (DeviceCMYK)", ...
	paletteRGB []byte // populated when kind == "indexed_rgb"
}

// resolveImageColorSpace decodes the /ColorSpace entry of an Image XObject.
// Returns an effectiveColorSpace whose kind == "" if the space is one we
// don't yet handle; label is still populated so the manifest can record it.
func (f *pdfFile) resolveImageColorSpace(v any) effectiveColorSpace {
	resolved := f.resolve(v)

	switch cs := resolved.(type) {
	case pdfName:
		switch cs {
		case "DeviceGray", "CalGray", "G":
			return effectiveColorSpace{kind: "gray", label: string(cs)}
		case "DeviceRGB", "CalRGB", "RGB":
			return effectiveColorSpace{kind: "rgb", label: string(cs)}
		case "DeviceCMYK", "CMYK":
			return effectiveColorSpace{kind: "cmyk", label: string(cs)}
		default:
			return effectiveColorSpace{label: string(cs)}
		}

	case pdfArray:
		if len(cs) == 0 {
			return effectiveColorSpace{}
		}
		head := f.getName(cs[0])
		switch head {
		case "ICCBased":
			return f.resolveICCBased(cs)
		case "Indexed", "I":
			return f.resolveIndexed(cs)
		case "DeviceGray", "CalGray", "G":
			return effectiveColorSpace{kind: "gray", label: head}
		case "DeviceRGB", "CalRGB", "RGB":
			return effectiveColorSpace{kind: "rgb", label: head}
		case "DeviceCMYK", "CMYK":
			return effectiveColorSpace{kind: "cmyk", label: head}
		default:
			return effectiveColorSpace{label: head}
		}
	}

	return effectiveColorSpace{}
}

// resolveICCBased maps an [/ICCBased <stream>] entry to its alternate Device
// space. The stream's /N (1/3/4) is authoritative per PDF 1.7 §8.6.5.5.
func (f *pdfFile) resolveICCBased(cs pdfArray) effectiveColorSpace {
	if len(cs) < 2 {
		return effectiveColorSpace{label: "ICCBased"}
	}
	stream, ok := f.getStream(cs[1])
	if !ok {
		return effectiveColorSpace{label: "ICCBased"}
	}
	n := f.getInt(stream.dict["N"], 0)
	switch n {
	case 1:
		return effectiveColorSpace{kind: "gray", label: "ICCBased (N=1)"}
	case 3:
		return effectiveColorSpace{kind: "rgb", label: "ICCBased (N=3)"}
	case 4:
		return effectiveColorSpace{kind: "cmyk", label: "ICCBased (N=4)"}
	default:
		return effectiveColorSpace{label: fmt.Sprintf("ICCBased (N=%d)", n)}
	}
}

// resolveIndexed flattens an [/Indexed <base> <hival> <lookup>] entry to an
// RGB palette. The base may be a Device* name, another array, or an ICCBased
// reference; hival is 0..255; lookup is either a literal byte string or an
// indirect reference to a stream containing the palette bytes.
func (f *pdfFile) resolveIndexed(cs pdfArray) effectiveColorSpace {
	if len(cs) < 4 {
		return effectiveColorSpace{label: "Indexed"}
	}

	base := f.resolveImageColorSpace(cs[1])
	hival := f.getInt(cs[2], 0)
	if hival < 0 || hival > 255 {
		return effectiveColorSpace{label: fmt.Sprintf("Indexed (%s, hival=%d)", base.label, hival)}
	}

	lookup := f.readPaletteBytes(cs[3])
	if lookup == nil {
		return effectiveColorSpace{label: fmt.Sprintf("Indexed (%s)", base.label)}
	}

	stride, ok := baseStride(base.kind)
	if !ok {
		return effectiveColorSpace{label: fmt.Sprintf("Indexed (%s)", base.label)}
	}
	need := (hival + 1) * stride
	if len(lookup) < need {
		return effectiveColorSpace{label: fmt.Sprintf("Indexed (%s, short palette)", base.label)}
	}

	paletteRGB := make([]byte, (hival+1)*3)
	for i := 0; i <= hival; i++ {
		entry := lookup[i*stride : i*stride+stride]
		r, g, b := paletteEntryToRGB(base.kind, entry)
		paletteRGB[i*3+0] = r
		paletteRGB[i*3+1] = g
		paletteRGB[i*3+2] = b
	}

	return effectiveColorSpace{
		kind:       "indexed_rgb",
		label:      fmt.Sprintf("Indexed (%s)", base.label),
		paletteRGB: paletteRGB,
	}
}

// readPaletteBytes returns the bytes of an Indexed /Lookup operand. PDF allows
// it to be a literal string OR an indirect ref to a stream.
func (f *pdfFile) readPaletteBytes(v any) []byte {
	resolved := f.resolve(v)
	switch lv := resolved.(type) {
	case pdfString:
		return []byte(lv)
	case pdfStream:
		decoded, err := f.decodeStream(lv)
		if err != nil {
			return nil
		}
		return decoded
	}
	return nil
}

// baseStride returns bytes-per-palette-entry for an indexed image's base
// colorspace.
func baseStride(kind string) (int, bool) {
	switch kind {
	case "gray":
		return 1, true
	case "rgb":
		return 3, true
	case "cmyk":
		return 4, true
	default:
		return 0, false
	}
}

// paletteEntryToRGB converts one palette entry (in the base colorspace) to
// 8-bit sRGB. Assumes 8-bit components in the entry, which is universal for
// the Device* base spaces used by Indexed in practice.
func paletteEntryToRGB(baseKind string, entry []byte) (byte, byte, byte) {
	switch baseKind {
	case "gray":
		return entry[0], entry[0], entry[0]
	case "rgb":
		return entry[0], entry[1], entry[2]
	case "cmyk":
		return cmykToRGB(entry[0], entry[1], entry[2], entry[3])
	}
	return 0, 0, 0
}

// cmykToRGB applies the standard non-color-managed CMYK→sRGB approximation:
//
//	r = (255 - C) * (255 - K) / 255
//	g = (255 - M) * (255 - K) / 255
//	b = (255 - Y) * (255 - K) / 255
//
// This loses gamut fidelity vs. an ICC-managed conversion but is correct for
// the pure-channel test cases and visually adequate for doctrinal figures.
func cmykToRGB(c, m, y, k byte) (byte, byte, byte) {
	kInv := 255 - int(k)
	r := (255 - int(c)) * kInv / 255
	g := (255 - int(m)) * kInv / 255
	b := (255 - int(y)) * kInv / 255
	return byte(r), byte(g), byte(b)
}

// encodeRasterAsPNG converts a raw raster buffer into a PNG. Returns
// (bytes, "", nil) on success. Returns (nil, reason, nil) for color
// spaces / bit depths not yet supported — reason is a short label for logging.
func encodeRasterAsPNG(raster []byte, w, h, bpc int, cs effectiveColorSpace) ([]byte, string, error) {
	if w <= 0 || h <= 0 {
		return nil, "", fmt.Errorf("invalid dimensions %dx%d", w, h)
	}
	if int64(w)*int64(h) > maxImagePixels {
		return nil, fmt.Sprintf("dimensions %dx%d exceed pixel ceiling %d", w, h, maxImagePixels), nil
	}

	if cs.kind == "" {
		return nil, fmt.Sprintf("colorspace %q not yet supported", cs.label), nil
	}

	var img image.Image

	switch cs.kind {
	case "gray":
		if bpc != 8 {
			return nil, fmt.Sprintf("%s at %d bpc not yet supported", cs.label, bpc), nil
		}
		if len(raster) < w*h {
			return nil, "", fmt.Errorf("gray raster too short: got %d, want >=%d", len(raster), w*h)
		}
		g := image.NewGray(image.Rect(0, 0, w, h))
		for y := range h {
			copy(g.Pix[y*g.Stride:y*g.Stride+w], raster[y*w:(y+1)*w])
		}
		img = g

	case "rgb":
		if bpc != 8 {
			return nil, fmt.Sprintf("%s at %d bpc not yet supported", cs.label, bpc), nil
		}
		if len(raster) < w*h*3 {
			return nil, "", fmt.Errorf("rgb raster too short: got %d, want >=%d", len(raster), w*h*3)
		}
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			src := raster[y*w*3 : (y+1)*w*3]
			dst := rgba.Pix[y*rgba.Stride : y*rgba.Stride+w*4]
			for x := range w {
				dst[x*4+0] = src[x*3+0]
				dst[x*4+1] = src[x*3+1]
				dst[x*4+2] = src[x*3+2]
				dst[x*4+3] = 0xFF
			}
		}
		img = rgba

	case "cmyk":
		if bpc != 8 {
			return nil, fmt.Sprintf("%s at %d bpc not yet supported", cs.label, bpc), nil
		}
		if len(raster) < w*h*4 {
			return nil, "", fmt.Errorf("cmyk raster too short: got %d, want >=%d", len(raster), w*h*4)
		}
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			src := raster[y*w*4 : (y+1)*w*4]
			dst := rgba.Pix[y*rgba.Stride : y*rgba.Stride+w*4]
			for x := range w {
				r, g, b := cmykToRGB(src[x*4+0], src[x*4+1], src[x*4+2], src[x*4+3])
				dst[x*4+0] = r
				dst[x*4+1] = g
				dst[x*4+2] = b
				dst[x*4+3] = 0xFF
			}
		}
		img = rgba

	case "indexed_rgb":
		indices, err := unpackIndices(raster, w, h, bpc)
		if err != nil {
			return nil, "", err
		}
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		paletteEntries := len(cs.paletteRGB) / 3
		for y := range h {
			dst := rgba.Pix[y*rgba.Stride : y*rgba.Stride+w*4]
			row := indices[y*w : (y+1)*w]
			for x := range w {
				idx := int(row[x])
				if idx >= paletteEntries {
					idx = paletteEntries - 1
				}
				dst[x*4+0] = cs.paletteRGB[idx*3+0]
				dst[x*4+1] = cs.paletteRGB[idx*3+1]
				dst[x*4+2] = cs.paletteRGB[idx*3+2]
				dst[x*4+3] = 0xFF
			}
		}
		img = rgba

	default:
		return nil, fmt.Sprintf("colorspace %q not yet supported", cs.label), nil
	}

	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); err != nil {
		return nil, "", fmt.Errorf("png encode: %w", err)
	}
	return buf.Bytes(), "", nil
}

// unpackIndices unpacks a bit-packed raster of palette indices into one byte
// per pixel (w*h bytes total). Supports the four PDF index widths: 1, 2, 4,
// and 8 bits per component. Rows are byte-aligned, per PDF spec.
func unpackIndices(raster []byte, w, h, bpc int) ([]byte, error) {
	if bpc == 8 {
		if len(raster) < w*h {
			return nil, fmt.Errorf("indexed raster too short: got %d, want >=%d", len(raster), w*h)
		}
		return raster[:w*h], nil
	}
	if bpc != 1 && bpc != 2 && bpc != 4 {
		return nil, fmt.Errorf("indexed: unsupported bpc %d", bpc)
	}

	rowBytes := (w*bpc + 7) / 8
	if len(raster) < rowBytes*h {
		return nil, fmt.Errorf("indexed raster too short: got %d, want >=%d", len(raster), rowBytes*h)
	}

	mask := byte((1 << bpc) - 1)
	out := make([]byte, w*h)

	for y := range h {
		rowStart := y * rowBytes
		dstStart := y * w
		for x := range w {
			bitPos := x * bpc
			b := raster[rowStart+bitPos/8]
			shift := 8 - bpc - (bitPos % 8)
			out[dstStart+x] = (b >> shift) & mask
		}
	}
	return out, nil
}

// wrapCCITTAsTIFF wraps a raw CCITT Group 3 or Group 4 bitstream in a minimal
// single-strip TIFF container so that off-the-shelf viewers can open it.
//
// PDF's CCITTFaxDecode /K parameter maps to TIFF Compression:
//
//	K <  0: pure two-dimensional (T.6 / Group 4) — TIFF compression = 4
//	K == 0: pure one-dimensional (T.4) — TIFF compression = 3
//	K >  0: mixed one-/two-dimensional — TIFF compression = 3 with T4Options
//
// We emit a tiny IFD with exactly the tags a reader needs.
func wrapCCITTAsTIFF(payload []byte, width, height int, parms pdfDict) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid CCITT dimensions %dx%d", width, height)
	}
	if int64(width)*int64(height) > maxImagePixels {
		return nil, fmt.Errorf("CCITT dimensions %dx%d exceed pixel ceiling %d", width, height, maxImagePixels)
	}

	k := 0
	if v, ok := parms["K"].(pdfNumber); ok {
		k = int(v)
	}
	cols := width
	if v, ok := parms["Columns"].(pdfNumber); ok {
		// /Columns is independent of /Width and not constrained by the
		// pixel-area check above. Reject values that would silently wrap
		// when cast to the uint32 TIFF ImageWidth tag or that would
		// exceed the same pixel ceiling we apply to width.
		if v < 0 || v > maxImagePixels {
			return nil, fmt.Errorf("CCITT /Columns %v out of range", float64(v))
		}
		cols = int(v)
	}
	blackIs1 := false
	if v, ok := parms["BlackIs1"].(pdfBool); ok {
		blackIs1 = bool(v)
	}

	compression := uint16(3) // T.4 / Group 3
	if k < 0 {
		compression = 4 // T.6 / Group 4
	}

	// PhotometricInterpretation: 0 = WhiteIsZero (PDF default), 1 = BlackIsZero.
	photometric := uint16(0)
	if blackIs1 {
		photometric = 1
	}

	const (
		ifdEntries     = 9
		ifdEntrySize   = 12
		headerSize     = 8
		ifdCountSize   = 2
		nextIFDPtrSize = 4
	)

	ifdSize := ifdCountSize + ifdEntries*ifdEntrySize + nextIFDPtrSize
	stripOffset := uint32(headerSize + ifdSize)
	stripByteCount := uint32(len(payload))

	var buf bytes.Buffer
	le := binary.LittleEndian

	// Header.
	buf.WriteString("II")
	_ = binary.Write(&buf, le, uint16(42))
	_ = binary.Write(&buf, le, uint32(headerSize))

	// IFD.
	_ = binary.Write(&buf, le, uint16(ifdEntries))

	writeEntry := func(tag, typ uint16, count, value uint32) {
		_ = binary.Write(&buf, le, tag)
		_ = binary.Write(&buf, le, typ)
		_ = binary.Write(&buf, le, count)
		_ = binary.Write(&buf, le, value)
	}

	const (
		typeShort = uint16(3)
		typeLong  = uint16(4)
	)

	writeEntry(256, typeLong, 1, uint32(cols))         // ImageWidth
	writeEntry(257, typeLong, 1, uint32(height))       // ImageLength
	writeEntry(258, typeShort, 1, 1)                   // BitsPerSample
	writeEntry(259, typeShort, 1, uint32(compression)) // Compression
	writeEntry(262, typeShort, 1, uint32(photometric)) // PhotometricInterpretation
	writeEntry(273, typeLong, 1, stripOffset)          // StripOffsets
	writeEntry(277, typeShort, 1, 1)                   // SamplesPerPixel
	writeEntry(278, typeLong, 1, uint32(height))       // RowsPerStrip (one strip)
	writeEntry(279, typeLong, 1, stripByteCount)       // StripByteCounts

	_ = binary.Write(&buf, le, uint32(0)) // next IFD pointer (none)

	buf.Write(payload)
	return buf.Bytes(), nil
}
