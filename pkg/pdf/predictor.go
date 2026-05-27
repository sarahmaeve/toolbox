package pdf

import "fmt"

// applyPredictor reverses the PNG or TIFF row prediction applied before
// FlateDecode compression. See PDF 1.7 §7.4.4.3 and PNG spec §6.
//
// PDF predictor values:
//
//	1     no prediction (pass-through)
//	2     TIFF Predictor 2 (subtract previous pixel in row) — not implemented
//	10–15 PNG predictors; the row's filter byte chooses per row:
//	      10 None, 11 Sub, 12 Up, 13 Average, 14 Paeth, 15 optimum.
//	      The decode path is identical for all PNG predictors — the row's
//	      first byte is authoritative regardless of the dict's Predictor value.
func applyPredictor(data []byte, parms pdfDict) ([]byte, error) {
	predictor := 1
	if p, ok := parms["Predictor"].(pdfNumber); ok {
		predictor = int(p)
	}
	if predictor == 1 {
		return data, nil
	}
	if predictor < 10 || predictor > 15 {
		return nil, fmt.Errorf("predictor %d not yet implemented", predictor)
	}

	columns := 1
	if v, ok := parms["Columns"].(pdfNumber); ok {
		columns = int(v)
	}
	colors := 1
	if v, ok := parms["Colors"].(pdfNumber); ok {
		colors = int(v)
	}
	bits := 8
	if v, ok := parms["BitsPerComponent"].(pdfNumber); ok {
		bits = int(v)
	}

	// bytes per pixel, rounded up to whole bytes.
	bpp := (colors*bits + 7) / 8
	if bpp < 1 {
		bpp = 1
	}
	rowBytes := (columns*colors*bits + 7) / 8
	stride := rowBytes + 1 // 1 filter byte per row

	if stride <= 1 {
		return nil, fmt.Errorf("predictor: invalid row stride %d (columns=%d colors=%d bits=%d)",
			stride, columns, colors, bits)
	}
	if len(data)%stride != 0 {
		return nil, fmt.Errorf("predictor: filtered length %d not a multiple of stride %d",
			len(data), stride)
	}

	nRows := len(data) / stride
	out := make([]byte, 0, nRows*rowBytes)
	prev := make([]byte, rowBytes) // initial previous row is all zeros

	row := make([]byte, rowBytes)

	for r := range nRows {
		base := r * stride
		filter := data[base]
		filtered := data[base+1 : base+stride]

		switch filter {
		case 0: // None
			copy(row, filtered)
		case 1: // Sub
			for i := range rowBytes {
				left := byte(0)
				if i >= bpp {
					left = row[i-bpp]
				}
				row[i] = filtered[i] + left
			}
		case 2: // Up
			for i := range rowBytes {
				row[i] = filtered[i] + prev[i]
			}
		case 3: // Average
			for i := range rowBytes {
				left := byte(0)
				if i >= bpp {
					left = row[i-bpp]
				}
				row[i] = filtered[i] + byte((int(left)+int(prev[i]))/2)
			}
		case 4: // Paeth
			for i := range rowBytes {
				var a, c byte
				if i >= bpp {
					a = row[i-bpp]
					c = prev[i-bpp]
				}
				b := prev[i]
				row[i] = filtered[i] + paethPredictor(a, b, c)
			}
		default:
			return nil, fmt.Errorf("predictor: unknown PNG filter byte %d at row %d", filter, r)
		}

		out = append(out, row...)
		// next iteration's prev is the row we just produced
		prev, row = row, prev
	}

	return out, nil
}

// paethPredictor is the PNG Paeth predictor (PNG spec §6.6).
func paethPredictor(a, b, c byte) byte {
	pa := absDiff(int(a)+int(b)-int(c), int(a))
	pb := absDiff(int(a)+int(b)-int(c), int(b))
	pc := absDiff(int(a)+int(b)-int(c), int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	default:
		return c
	}
}

func absDiff(x, y int) int {
	d := x - y
	if d < 0 {
		return -d
	}
	return d
}
