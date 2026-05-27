package pdf

import "fmt"

// parseObjStmContents decodes the prefix of a /Type /ObjStm stream body and
// returns the index of contained objects plus the body bytes that hold the
// actual values. n is the /N entry (object count), first is the /First entry
// (byte offset where the body begins, relative to data[0]).
//
// The prefix layout is N pairs of ASCII integers: "objNum offset objNum offset ...".
// Each offset is relative to the start of the body (i.e., data[first:]).
func parseObjStmContents(data []byte, n, first int) (*objStm, error) {
	if first < 0 || first > len(data) {
		return nil, fmt.Errorf("objstm: /First %d out of range (len=%d)", first, len(data))
	}
	if n < 0 {
		return nil, fmt.Errorf("objstm: negative /N %d", n)
	}

	pairs := make([][2]int, 0, n)
	pos := 0
	prefix := data[:first]

	for range n {
		pos = skipWhitespace(prefix, pos)
		objNum, newPos, err := readInt(prefix, pos)
		if err != nil {
			return nil, fmt.Errorf("objstm: reading obj number in prefix (parsed %d/%d pairs): %w",
				len(pairs), n, err)
		}
		pos = skipWhitespace(prefix, newPos)
		offset, newPos, err := readInt(prefix, pos)
		if err != nil {
			return nil, fmt.Errorf("objstm: reading offset for obj %d: %w", objNum, err)
		}
		pos = newPos
		pairs = append(pairs, [2]int{objNum, offset})
	}

	body := make([]byte, len(data)-first)
	copy(body, data[first:])

	return &objStm{body: body, pairs: pairs}, nil
}

// readCompressedObject extracts object num from object stream streamNum at idx
// within that stream.
func (f *pdfFile) readCompressedObject(num, streamNum, idx int) (any, error) {
	stm, err := f.loadObjStm(streamNum)
	if err != nil {
		return nil, err
	}

	if idx < 0 || idx >= len(stm.pairs) {
		return nil, fmt.Errorf("objstm %d: index %d out of range (have %d objects)",
			streamNum, idx, len(stm.pairs))
	}
	pair := stm.pairs[idx]
	if pair[0] != num {
		return nil, fmt.Errorf("objstm %d index %d: stored objNum %d does not match requested %d",
			streamNum, idx, pair[0], num)
	}

	offset := pair[1]
	if offset < 0 || offset > len(stm.body) {
		return nil, fmt.Errorf("objstm %d obj %d: offset %d out of body range (len=%d)",
			streamNum, num, offset, len(stm.body))
	}

	val, _, err := parseValue(stm.body, offset, f)
	if err != nil {
		return nil, fmt.Errorf("objstm %d obj %d at offset %d: %w", streamNum, num, offset, err)
	}
	return val, nil
}

// loadObjStm returns the parsed object stream identified by streamNum,
// loading and caching it on first access.
func (f *pdfFile) loadObjStm(streamNum int) (*objStm, error) {
	if cached, ok := f.objStms[streamNum]; ok {
		return cached, nil
	}

	// The host object stream itself is an uncompressed indirect object;
	// fetch it through the normal xref + readObject pipeline.
	raw, err := f.readObject(streamNum)
	if err != nil {
		return nil, fmt.Errorf("loading objstm %d: %w", streamNum, err)
	}
	stream, ok := raw.(pdfStream)
	if !ok {
		return nil, fmt.Errorf("object %d is not a stream (got %T)", streamNum, raw)
	}
	if t, _ := stream.dict["Type"].(pdfName); t != "ObjStm" {
		return nil, fmt.Errorf("object %d /Type is %q, expected ObjStm", streamNum, t)
	}

	n := f.getInt(stream.dict["N"], -1)
	first := f.getInt(stream.dict["First"], -1)
	if n < 0 || first < 0 {
		return nil, fmt.Errorf("objstm %d missing /N or /First (N=%d First=%d)",
			streamNum, n, first)
	}

	decoded, err := f.decodeStream(stream)
	if err != nil {
		return nil, fmt.Errorf("decoding objstm %d: %w", streamNum, err)
	}

	stm, err := parseObjStmContents(decoded, n, first)
	if err != nil {
		return nil, fmt.Errorf("parsing objstm %d: %w", streamNum, err)
	}

	// Cache before returning so a concurrent or recursive resolve sees it.
	f.objStms[streamNum] = stm

	// Also record the host stream itself in the resolved-object cache so we
	// don't repeatedly re-parse the indirect object header.
	f.cache[streamNum] = stream

	return stm, nil
}
