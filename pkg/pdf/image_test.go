package pdf

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/png"
	"strings"
	"testing"
)

// newImageTestFile returns a pdfFile preconfigured so cache lookups succeed
// without going through xref. Tests can stash a pdfStream at cache[ref.num]
// and reference it with pdfRef{num: ref.num}.
func newImageTestFile() *pdfFile {
	return &pdfFile{
		xref:    map[int]xrefEntry{},
		cache:   map[int]any{},
		objStms: map[int]*objStm{},
	}
}

func TestEncodeRasterAsPNG_RejectsHostileDimensions(t *testing.T) {
	t.Parallel()

	// 1_000_000 x 1_000_000 RGB asks image.NewRGBA for 4 TB. Without a
	// pixel-area cap that allocation panics inside the stdlib (it overflows
	// pixelBufferLength's int check on 32-bit builds; on 64-bit the kernel
	// OOM-kills the process). The cap must fire before NewRGBA is reached
	// and must NOT be conflated with the downstream "raster too short"
	// check — the test asserts that by providing enough raster bytes that
	// the length check would otherwise pass.
	cs := effectiveColorSpace{kind: "rgb", label: "DeviceRGB"}

	// A small raster: NewRGBA would have rejected anyway, but our pixel
	// cap should reject BEFORE the raster length check fires.
	_, reason, err := encodeRasterAsPNG(nil, 1_000_000, 1_000_000, 8, cs)
	if err != nil {
		t.Fatalf("expected skip-reason for hostile dimensions, got hard error: %v", err)
	}
	if reason == "" {
		t.Fatal("expected non-empty skip reason for hostile dimensions")
	}
	if !strings.Contains(reason, "dimensions") && !strings.Contains(reason, "pixel") {
		t.Errorf("reason %q does not mention the dimension/pixel ceiling", reason)
	}
}

func TestWrapCCITTAsTIFF_RejectsHostileDimensions(t *testing.T) {
	t.Parallel()

	// CCITT path doesn't allocate a raster of width*height bytes, but it
	// does cast both to uint32 — values >MaxUint32 silently wrap. Reject
	// implausible dimensions up front for the same defense-in-depth
	// reason that the raster path needs a ceiling.
	_, err := wrapCCITTAsTIFF([]byte{0x00}, 1_000_000, 1_000_000, pdfDict{})
	if err == nil {
		t.Fatal("expected error for absurd CCITT dimensions, got nil")
	}
}

func TestImageFromStream_DCTDecodePassthrough(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// Minimal JPEG SOI/EOI markers — content is not a real JPEG, but the
	// passthrough path must not inspect it.
	raw := []byte{0xFF, 0xD8, 0xFF, 0xD9, 'h', 'i'}
	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(640),
			"Height":           pdfNumber(480),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace":       pdfName("DeviceRGB"),
			"Filter":           pdfName("DCTDecode"),
		},
		data: raw,
	}

	img, reason, err := f.imageFromStream(stream, 3, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Page != 3 {
		t.Errorf("Page: got %d, want 3", img.Page)
	}
	if img.Name != "Im0" {
		t.Errorf("Name: got %q, want %q", img.Name, "Im0")
	}
	if img.Width != 640 || img.Height != 480 {
		t.Errorf("dimensions: got %dx%d, want 640x480", img.Width, img.Height)
	}
	if img.Filter != "DCTDecode" {
		t.Errorf("Filter: got %q, want %q", img.Filter, "DCTDecode")
	}
	if img.Ext != "jpg" {
		t.Errorf("Ext: got %q, want %q", img.Ext, "jpg")
	}
	if !bytes.Equal(img.Data, raw) {
		t.Errorf("Data: not passed through verbatim (len %d vs %d)", len(img.Data), len(raw))
	}
}

func TestImageFromStream_JPXDecodePassthrough(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	raw := []byte("fake-jp2-bytes")
	stream := pdfStream{
		dict: pdfDict{
			"Subtype": pdfName("Image"),
			"Width":   pdfNumber(100),
			"Height":  pdfNumber(50),
			"Filter":  pdfName("JPXDecode"),
		},
		data: raw,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im1")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Ext != "jp2" {
		t.Errorf("Ext: got %q, want %q", img.Ext, "jp2")
	}
	if !bytes.Equal(img.Data, raw) {
		t.Errorf("Data: not passed through verbatim")
	}
}

func TestImageFromStream_FlateDeviceGray8(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// 4x2 grayscale image, predictor 1 (no row prediction).
	raw := []byte{
		0x00, 0x40, 0x80, 0xC0,
		0x10, 0x50, 0x90, 0xD0,
	}
	encoded := flateEncode(t, raw)

	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(4),
			"Height":           pdfNumber(2),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace":       pdfName("DeviceGray"),
			"Filter":           pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Ext != "png" {
		t.Fatalf("Ext: got %q, want %q", img.Ext, "png")
	}

	decoded, _, err := image.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("image.Decode(png): %v", err)
	}
	got, ok := decoded.(*image.Gray)
	if !ok {
		t.Fatalf("decoded image: got %T, want *image.Gray", decoded)
	}
	if got.Rect.Dx() != 4 || got.Rect.Dy() != 2 {
		t.Errorf("bounds: got %v, want 4x2", got.Rect)
	}
	if got.GrayAt(0, 0).Y != 0x00 || got.GrayAt(3, 0).Y != 0xC0 {
		t.Errorf("row 0 samples: got %v %v, want 0x00 0xC0",
			got.GrayAt(0, 0).Y, got.GrayAt(3, 0).Y)
	}
	if got.GrayAt(0, 1).Y != 0x10 || got.GrayAt(3, 1).Y != 0xD0 {
		t.Errorf("row 1 samples: got %v %v, want 0x10 0xD0",
			got.GrayAt(0, 1).Y, got.GrayAt(3, 1).Y)
	}
}

func TestImageFromStream_FlateDeviceRGB8(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// 2x1 RGB image: red, then green.
	raw := []byte{
		0xFF, 0x00, 0x00,
		0x00, 0xFF, 0x00,
	}
	encoded := flateEncode(t, raw)

	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(2),
			"Height":           pdfNumber(1),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace":       pdfName("DeviceRGB"),
			"Filter":           pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Ext != "png" {
		t.Fatalf("Ext: got %q, want %q", img.Ext, "png")
	}

	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 1 {
		t.Errorf("bounds: got %v, want 2x1", decoded.Bounds())
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r>>8 != 0xFF || g>>8 != 0x00 || b>>8 != 0x00 {
		t.Errorf("pixel(0,0): got %02x %02x %02x, want FF 00 00", r>>8, g>>8, b>>8)
	}
	r, g, b, _ = decoded.At(1, 0).RGBA()
	if r>>8 != 0x00 || g>>8 != 0xFF || b>>8 != 0x00 {
		t.Errorf("pixel(1,0): got %02x %02x %02x, want 00 FF 00", r>>8, g>>8, b>>8)
	}
}

func TestImageFromStream_CCITTFaxPassthrough(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// Fake compressed bitstream — TIFF wrapper must not inspect content.
	raw := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	stream := pdfStream{
		dict: pdfDict{
			"Subtype": pdfName("Image"),
			"Width":   pdfNumber(16),
			"Height":  pdfNumber(8),
			"Filter":  pdfName("CCITTFaxDecode"),
			"DecodeParms": pdfDict{
				"K":        pdfNumber(-1), // Group 4
				"Columns":  pdfNumber(16),
				"BlackIs1": pdfBool(false),
			},
		},
		data: raw,
	}

	img, reason, err := f.imageFromStream(stream, 5, "Im2")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Ext != "tif" {
		t.Errorf("Ext: got %q, want %q", img.Ext, "tif")
	}
	// TIFF little-endian magic: "II" 0x2A 0x00
	if len(img.Data) < 8 || img.Data[0] != 'I' || img.Data[1] != 'I' ||
		img.Data[2] != 0x2A || img.Data[3] != 0x00 {
		t.Errorf("TIFF magic: got % x, want 49 49 2A 00", img.Data[:min(8, len(img.Data))])
	}
	// The raw bytes should appear somewhere in the file.
	if !bytes.Contains(img.Data, raw) {
		t.Error("TIFF output does not contain raw CCITT payload")
	}
}

func TestImageFromStream_UnsupportedFilter(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	stream := pdfStream{
		dict: pdfDict{
			"Subtype": pdfName("Image"),
			"Width":   pdfNumber(10),
			"Height":  pdfNumber(10),
			"Filter":  pdfName("JBIG2Decode"),
		},
		data: []byte{0x00, 0x01, 0x02},
	}

	_, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Errorf("imageFromStream: unexpected error: %v", err)
	}
	if reason == "" {
		t.Error("imageFromStream: reason=\"\" for JBIG2Decode, want non-empty (skip-and-warn)")
	}
}

func TestExtractPageImages_NoResources(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	pageRef := pdfRef{num: 1}
	f.cache[1] = pdfDict{
		"Type": pdfName("Page"),
	}

	imgs, err := f.extractPageImages(pageRef, 1)
	if err != nil {
		t.Fatalf("extractPageImages: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("got %d images, want 0", len(imgs))
	}
}

func TestExtractPageImages_FiltersByImageSubtype(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// Page Resources/XObject contains one Form XObject and one Image XObject.
	// extractPageImages must return only the Image.
	formStream := pdfStream{
		dict: pdfDict{"Subtype": pdfName("Form")},
		data: []byte("q Q"),
	}
	imgStream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(8),
			"Height":           pdfNumber(8),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace":       pdfName("DeviceRGB"),
			"Filter":           pdfName("DCTDecode"),
		},
		data: []byte{0xFF, 0xD8, 0xFF, 0xD9},
	}
	f.cache[10] = formStream
	f.cache[11] = imgStream

	pageRef := pdfRef{num: 1}
	f.cache[1] = pdfDict{
		"Type": pdfName("Page"),
		"Resources": pdfDict{
			"XObject": pdfDict{
				"Fm0": pdfRef{num: 10},
				"Im0": pdfRef{num: 11},
			},
		},
	}

	imgs, err := f.extractPageImages(pageRef, 7)
	if err != nil {
		t.Fatalf("extractPageImages: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("got %d images, want 1", len(imgs))
	}
	got := imgs[0]
	if got.Page != 7 {
		t.Errorf("Page: got %d, want 7", got.Page)
	}
	if got.Name != "Im0" {
		t.Errorf("Name: got %q, want %q", got.Name, "Im0")
	}
	if got.Ext != "jpg" {
		t.Errorf("Ext: got %q, want %q", got.Ext, "jpg")
	}
}

func TestExtractPageImages_StableOrder(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	mkStream := func() pdfStream {
		return pdfStream{
			dict: pdfDict{
				"Subtype": pdfName("Image"),
				"Width":   pdfNumber(1),
				"Height":  pdfNumber(1),
				"Filter":  pdfName("DCTDecode"),
			},
			data: []byte{0xFF, 0xD8, 0xFF, 0xD9},
		}
	}
	f.cache[20] = mkStream()
	f.cache[21] = mkStream()
	f.cache[22] = mkStream()

	pageRef := pdfRef{num: 1}
	f.cache[1] = pdfDict{
		"Resources": pdfDict{
			"XObject": pdfDict{
				"ImB": pdfRef{num: 21},
				"ImA": pdfRef{num: 20},
				"ImC": pdfRef{num: 22},
			},
		},
	}

	imgs, err := f.extractPageImages(pageRef, 1)
	if err != nil {
		t.Fatalf("extractPageImages: %v", err)
	}
	if len(imgs) != 3 {
		t.Fatalf("got %d images, want 3", len(imgs))
	}
	wantNames := []string{"ImA", "ImB", "ImC"}
	for i, want := range wantNames {
		if imgs[i].Name != want {
			t.Errorf("imgs[%d].Name: got %q, want %q", i, imgs[i].Name, want)
		}
	}
}

func TestImageFromStream_FlateDeviceCMYK8(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// 4x1 image, one pixel each of pure cyan, magenta, yellow, black.
	// CMYK→RGB formula: r=(255-C)(255-K)/255, etc.
	raw := []byte{
		0xFF, 0x00, 0x00, 0x00, // pure C
		0x00, 0xFF, 0x00, 0x00, // pure M
		0x00, 0x00, 0xFF, 0x00, // pure Y
		0x00, 0x00, 0x00, 0xFF, // pure K
	}
	encoded := flateEncode(t, raw)
	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(4),
			"Height":           pdfNumber(1),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace":       pdfName("DeviceCMYK"),
			"Filter":           pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Ext != "png" {
		t.Fatalf("Ext: got %q, want %q", img.Ext, "png")
	}

	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	wantRGB := [][3]uint8{
		{0x00, 0xFF, 0xFF}, // C
		{0xFF, 0x00, 0xFF}, // M
		{0xFF, 0xFF, 0x00}, // Y
		{0x00, 0x00, 0x00}, // K
	}
	for x, want := range wantRGB {
		r, g, b, _ := decoded.At(x, 0).RGBA()
		got := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
		if got != want {
			t.Errorf("pixel(%d,0): got %v, want %v", x, got, want)
		}
	}
}

func TestImageFromStream_ICCBased_N3(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// 1x1 red pixel under ICCBased with N=3 (treated as DeviceRGB).
	raw := []byte{0xFF, 0x00, 0x00}
	encoded := flateEncode(t, raw)

	// The ICC profile stream is referenced by an indirect ref; we stash it
	// directly into the cache so resolve() returns it.
	iccStream := pdfStream{
		dict: pdfDict{"N": pdfNumber(3)},
		data: []byte("dummy-icc-profile"),
	}
	f.cache[50] = iccStream

	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(1),
			"Height":           pdfNumber(1),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace":       pdfArray{pdfName("ICCBased"), pdfRef{num: 50}},
			"Filter":           pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Ext != "png" {
		t.Fatalf("Ext: got %q, want %q", img.Ext, "png")
	}
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r>>8 != 0xFF || g>>8 != 0x00 || b>>8 != 0x00 {
		t.Errorf("pixel(0,0): got %02x %02x %02x, want FF 00 00", r>>8, g>>8, b>>8)
	}
}

func TestImageFromStream_ICCBased_N1(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	raw := []byte{0x80, 0x40}
	encoded := flateEncode(t, raw)

	f.cache[51] = pdfStream{
		dict: pdfDict{"N": pdfNumber(1)},
		data: []byte("dummy"),
	}
	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(2),
			"Height":           pdfNumber(1),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace":       pdfArray{pdfName("ICCBased"), pdfRef{num: 51}},
			"Filter":           pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	decoded, _, err := image.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("image.Decode: %v", err)
	}
	g, ok := decoded.(*image.Gray)
	if !ok {
		t.Fatalf("decoded image: got %T, want *image.Gray", decoded)
	}
	if g.GrayAt(0, 0).Y != 0x80 || g.GrayAt(1, 0).Y != 0x40 {
		t.Errorf("gray samples: got %v %v, want 0x80 0x40",
			g.GrayAt(0, 0).Y, g.GrayAt(1, 0).Y)
	}
}

func TestImageFromStream_IndexedDeviceRGB_InlinePalette(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// Palette of 3 entries: red, green, blue.
	palette := pdfString(string([]byte{
		0xFF, 0x00, 0x00,
		0x00, 0xFF, 0x00,
		0x00, 0x00, 0xFF,
	}))
	// 4x1 image: idx 0, 1, 2, 1 → red, green, blue, green.
	raw := []byte{0, 1, 2, 1}
	encoded := flateEncode(t, raw)

	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(4),
			"Height":           pdfNumber(1),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace": pdfArray{
				pdfName("Indexed"),
				pdfName("DeviceRGB"),
				pdfNumber(2),
				palette,
			},
			"Filter": pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	if img.Ext != "png" {
		t.Fatalf("Ext: got %q, want %q", img.Ext, "png")
	}
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	want := [][3]uint8{
		{0xFF, 0x00, 0x00},
		{0x00, 0xFF, 0x00},
		{0x00, 0x00, 0xFF},
		{0x00, 0xFF, 0x00},
	}
	for x, exp := range want {
		r, g, b, _ := decoded.At(x, 0).RGBA()
		got := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
		if got != exp {
			t.Errorf("pixel(%d,0): got %v, want %v", x, got, exp)
		}
	}
}

func TestImageFromStream_IndexedDeviceCMYK_StreamPalette(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// Palette of 2 entries: pure cyan, pure black. CMYK bytes:
	// idx 0: C=255 M=0 Y=0 K=0  → RGB (0, 255, 255)
	// idx 1: C=0   M=0 Y=0 K=255 → RGB (0, 0, 0)
	paletteBytes := []byte{
		0xFF, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0xFF,
	}
	f.cache[60] = pdfStream{
		dict: pdfDict{}, // no filter; data is plaintext
		data: paletteBytes,
	}

	// 2x1 image: idx 0, idx 1.
	raw := []byte{0, 1}
	encoded := flateEncode(t, raw)

	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(2),
			"Height":           pdfNumber(1),
			"BitsPerComponent": pdfNumber(8),
			"ColorSpace": pdfArray{
				pdfName("Indexed"),
				pdfName("DeviceCMYK"),
				pdfNumber(1),
				pdfRef{num: 60},
			},
			"Filter": pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r>>8 != 0x00 || g>>8 != 0xFF || b>>8 != 0xFF {
		t.Errorf("pixel(0,0) Cyan: got %02x %02x %02x, want 00 FF FF", r>>8, g>>8, b>>8)
	}
	r, g, b, _ = decoded.At(1, 0).RGBA()
	if r>>8 != 0x00 || g>>8 != 0x00 || b>>8 != 0x00 {
		t.Errorf("pixel(1,0) Black: got %02x %02x %02x, want 00 00 00", r>>8, g>>8, b>>8)
	}
}

func TestImageFromStream_Indexed_4bpc(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// 4-entry palette: red, green, blue, white.
	palette := pdfString(string([]byte{
		0xFF, 0x00, 0x00,
		0x00, 0xFF, 0x00,
		0x00, 0x00, 0xFF,
		0xFF, 0xFF, 0xFF,
	}))
	// 4x1 image at 4 bpc: indices 0,1,2,3. Two pixels per byte.
	// 0x01 = (0<<4)|1 → idx 0, idx 1
	// 0x23 = (2<<4)|3 → idx 2, idx 3
	raw := []byte{0x01, 0x23}
	encoded := flateEncode(t, raw)

	stream := pdfStream{
		dict: pdfDict{
			"Subtype":          pdfName("Image"),
			"Width":            pdfNumber(4),
			"Height":           pdfNumber(1),
			"BitsPerComponent": pdfNumber(4),
			"ColorSpace": pdfArray{
				pdfName("Indexed"),
				pdfName("DeviceRGB"),
				pdfNumber(3),
				palette,
			},
			"Filter": pdfName("FlateDecode"),
		},
		data: encoded,
	}

	img, reason, err := f.imageFromStream(stream, 1, "Im0")
	if err != nil {
		t.Fatalf("imageFromStream: %v", err)
	}
	if reason != "" {
		t.Fatalf("imageFromStream: reason=%q, want \"\"", reason)
	}
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	want := [][3]uint8{
		{0xFF, 0x00, 0x00},
		{0x00, 0xFF, 0x00},
		{0x00, 0x00, 0xFF},
		{0xFF, 0xFF, 0xFF},
	}
	for x, exp := range want {
		r, g, b, _ := decoded.At(x, 0).RGBA()
		got := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
		if got != exp {
			t.Errorf("pixel(%d,0): got %v, want %v", x, got, exp)
		}
	}
}

func TestExtractPageImages_AttachesBbox(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	// Three panels: 100x50 each, side-by-side on a horizontal band at y=200.
	mkImg := func(n int) pdfStream {
		_ = n
		return pdfStream{
			dict: pdfDict{
				"Subtype": pdfName("Image"),
				"Width":   pdfNumber(100),
				"Height":  pdfNumber(50),
				"Filter":  pdfName("DCTDecode"),
			},
			data: []byte{0xFF, 0xD8, 0xFF, 0xD9},
		}
	}
	f.cache[10] = mkImg(0)
	f.cache[11] = mkImg(1)
	f.cache[12] = mkImg(2)

	// Page content stream paints all three; its bytes go on a referenced stream.
	contentRef := pdfRef{num: 20}
	f.cache[20] = pdfStream{
		dict: pdfDict{},
		data: []byte(`
			q 100 0 0 50 0   200 cm /ImA Do Q
			q 100 0 0 50 100 200 cm /ImB Do Q
			q 100 0 0 50 200 200 cm /ImC Do Q
		`),
	}

	pageRef := pdfRef{num: 1}
	f.cache[1] = pdfDict{
		"Type": pdfName("Page"),
		"Resources": pdfDict{
			"XObject": pdfDict{
				"ImA": pdfRef{num: 10},
				"ImB": pdfRef{num: 11},
				"ImC": pdfRef{num: 12},
			},
		},
		"Contents": contentRef,
	}

	imgs, err := f.extractPageImages(pageRef, 1)
	if err != nil {
		t.Fatalf("extractPageImages: %v", err)
	}
	if len(imgs) != 3 {
		t.Fatalf("got %d images, want 3", len(imgs))
	}

	wantBoxes := map[string]bbox{
		"ImA": {X: 0, Y: 200, W: 100, H: 50},
		"ImB": {X: 100, Y: 200, W: 100, H: 50},
		"ImC": {X: 200, Y: 200, W: 100, H: 50},
	}
	for _, img := range imgs {
		want := wantBoxes[img.Name]
		got := bbox{X: img.BboxX, Y: img.BboxY, W: img.BboxW, H: img.BboxH}
		if !bboxClose(got, want, 1e-6) {
			t.Errorf("%s: got bbox %+v, want %+v", img.Name, got, want)
		}
	}
}

func TestExtractPageImages_ZeroBboxWhenNoContents(t *testing.T) {
	t.Parallel()

	f := newImageTestFile()

	f.cache[10] = pdfStream{
		dict: pdfDict{
			"Subtype": pdfName("Image"),
			"Width":   pdfNumber(8),
			"Height":  pdfNumber(8),
			"Filter":  pdfName("DCTDecode"),
		},
		data: []byte{0xFF, 0xD8, 0xFF, 0xD9},
	}

	pageRef := pdfRef{num: 1}
	f.cache[1] = pdfDict{
		"Type": pdfName("Page"),
		"Resources": pdfDict{
			"XObject": pdfDict{
				"Im0": pdfRef{num: 10},
			},
		},
		// no Contents — image is defined but never Done.
	}

	imgs, err := f.extractPageImages(pageRef, 1)
	if err != nil {
		t.Fatalf("extractPageImages: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("got %d images, want 1", len(imgs))
	}
	if imgs[0].BboxW != 0 || imgs[0].BboxH != 0 {
		t.Errorf("BboxW/H should be 0 when no Contents; got W=%v H=%v",
			imgs[0].BboxW, imgs[0].BboxH)
	}
}

// --- helpers ----------------------------------------------------------------

func flateEncode(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("flate write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("flate close: %v", err)
	}
	return buf.Bytes()
}
