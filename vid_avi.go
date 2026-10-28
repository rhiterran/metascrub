// AVI (RIFF).
//
// Only the chunks needed for playback are kept as they are (headers, stream
// headers/formats, indexes, the movie data). Everything else — INFO tags
// (title, software, comments), recording dates (IDIT), stream names, XMP and
// camera-maker chunks — becomes a same-size zero-filled JUNK chunk.
package main

import "encoding/binary"

type aviPlanner struct {
	rd
	p *plan
}

func planAVI(r rd) (*plan, error) {
	a := &aviPlanner{rd: r, p: &plan{}}
	var off int64
	for i := 0; off+12 <= a.size; i++ {
		h, err := a.bytes(off, 12)
		if err != nil || string(h[:4]) != "RIFF" {
			if i == 0 {
				return nil, errTrunc
			}
			break
		}
		size := int64(binary.LittleEndian.Uint32(h[4:]))
		end := off + 8 + size
		if end > a.size {
			end = a.size
		}
		ctx := "riff"
		if string(h[8:12]) == "AVIX" {
			ctx = "avix"
		}
		a.walk(off+12, end, ctx)
		off += 8 + size + size&1
	}
	return a.p, nil
}

func (a *aviPlanner) walk(start, end int64, ctx string) {
	for off := start; off+8 <= end; {
		h, err := a.bytes(off, 8)
		if err != nil {
			return
		}
		id := string(h[:4])
		size := int64(binary.LittleEndian.Uint32(h[4:]))
		if off+8+size > end {
			return // truncated: leave the rest as is
		}
		list := ""
		if id == "LIST" && size >= 4 {
			if lt, err := a.bytes(off+8, 4); err == nil {
				list = string(lt)
			}
		}
		keep, into := aviRule(ctx, id, list)
		switch {
		case into != "":
			a.walk(off+12, off+8+size, into)
		case !keep:
			a.p.put(off, []byte("JUNK"))
			a.p.fillRange(off+8, size, 0)
			a.p.note(aviLabel(id, list))
		}
		off += 8 + size + size&1
	}
}

// aviRule says whether a chunk is kept, or which context to recurse into.
func aviRule(ctx, id, list string) (keep bool, into string) {
	if id == "JUNK" || id == "JUNQ" {
		return true, ""
	}
	switch ctx {
	case "riff":
		switch {
		case id == "LIST" && list == "hdrl":
			return true, "hdrl"
		case id == "LIST" && list == "movi", id == "idx1":
			return true, ""
		}
	case "avix":
		if id == "LIST" && list == "movi" {
			return true, ""
		}
	case "hdrl":
		switch {
		case id == "avih", id == "LIST" && list == "odml":
			return true, ""
		case id == "LIST" && list == "strl":
			return true, "strl"
		}
	case "strl":
		switch id {
		case "strh", "strf", "strd", "indx", "vprp":
			return true, ""
		}
	}
	return false, ""
}

func aviLabel(id, list string) string {
	switch {
	case id == "LIST" && list == "INFO":
		return "INFO tags (title, software, comments)"
	case id == "IDIT":
		return "recording date"
	case id == "strn":
		return "stream names"
	case id == "_PMX":
		return "XMP"
	}
	return "vendor metadata"
}
