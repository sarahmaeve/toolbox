package pdf

// bbox is an axis-aligned rectangle in PDF page coordinates (points,
// lower-left origin). Width and height are always non-negative.
type bbox struct {
	X, Y, W, H float64
}

// xobjectPlacement records one Do invocation of an XObject in a content
// stream, along with the page-coordinate bounding box where it was painted.
type xobjectPlacement struct {
	name string
	box  bbox
}

// ctm is the 6-element affine matrix used by PDF content streams. The full
// 3x3 matrix is:
//
//	[ a b 0 ]
//	[ c d 0 ]
//	[ e f 1 ]
//
// A point (x, y) is mapped to (a*x + c*y + e, b*x + d*y + f).
type ctm struct{ a, b, c, d, e, f float64 }

var identityCTM = ctm{a: 1, b: 0, c: 0, d: 1, e: 0, f: 0}

// concat returns m * outer — i.e. m applied "inside" outer. This is the
// semantic of PDF's cm operator: the new CTM is local_matrix × current_CTM.
func (m ctm) concat(outer ctm) ctm {
	return ctm{
		a: m.a*outer.a + m.b*outer.c,
		b: m.a*outer.b + m.b*outer.d,
		c: m.c*outer.a + m.d*outer.c,
		d: m.c*outer.b + m.d*outer.d,
		e: m.e*outer.a + m.f*outer.c + outer.e,
		f: m.e*outer.b + m.f*outer.d + outer.f,
	}
}

// apply maps a point through the matrix.
func (m ctm) apply(x, y float64) (float64, float64) {
	return m.a*x + m.c*y + m.e, m.b*x + m.d*y + m.f
}

// unitSquareBBox returns the axis-aligned bounding box of the unit square
// (0,0)–(1,1) after this CTM is applied. PDF Image XObjects are defined in
// unit-square space, so this gives the page-coordinate bbox of the painted
// image. Correct for rotated and skewed placements as well as upright ones.
func (m ctm) unitSquareBBox() bbox {
	x0, y0 := m.apply(0, 0)
	x1, y1 := m.apply(1, 0)
	x2, y2 := m.apply(0, 1)
	x3, y3 := m.apply(1, 1)

	minX, maxX := minMax4(x0, x1, x2, x3)
	minY, maxY := minMax4(y0, y1, y2, y3)
	return bbox{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
}

func minMax4(a, b, c, d float64) (float64, float64) {
	lo, hi := a, a
	for _, v := range [3]float64{b, c, d} {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

// walkXObjectPlacements parses a decoded page content stream and returns the
// page-coordinate bbox of every XObject Do invocation, in stream order.
//
// It tracks the subset of the PDF content-stream state machine needed for
// image placement:
//
//	cm   — concatenate matrix onto the current CTM
//	q    — push the CTM onto a graphics-state stack
//	Q    — pop the CTM
//	Do   — record the named XObject's placement at the current CTM
//
// All other operators are accepted but ignored; their operands are silently
// consumed so the operand stack stays in sync.
func walkXObjectPlacements(content []byte) []xobjectPlacement {
	var (
		current    = identityCTM
		stack      []ctm
		operands   []token
		placements []xobjectPlacement
	)

	pos := 0
	for pos < len(content) {
		pos = skipContentWS(content, pos)
		if pos >= len(content) {
			break
		}

		tok, next, ok := readToken(content, pos)
		if !ok {
			pos++
			continue
		}
		pos = next

		if tok.kind != tokOperator {
			operands = append(operands, tok)
			continue
		}

		switch tok.s {
		case "q":
			stack = append(stack, current)

		case "Q":
			if n := len(stack); n > 0 {
				current = stack[n-1]
				stack = stack[:n-1]
			}

		case "cm":
			if len(operands) >= 6 {
				ops := operands[len(operands)-6:]
				local := ctm{
					a: ops[0].n, b: ops[1].n,
					c: ops[2].n, d: ops[3].n,
					e: ops[4].n, f: ops[5].n,
				}
				current = local.concat(current)
			}

		case "Do":
			if len(operands) >= 1 {
				nameTok := operands[len(operands)-1]
				if nameTok.kind == tokName {
					name := nameTok.s
					if len(name) > 0 && name[0] == '/' {
						name = name[1:]
					}
					placements = append(placements, xobjectPlacement{
						name: name,
						box:  current.unitSquareBBox(),
					})
				}
			}
		}

		operands = operands[:0]
	}

	return placements
}
