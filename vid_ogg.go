// OGV / Ogg (Theora, Vorbis, Opus, FLAC, Speex streams).
//
// Ogg metadata is the comment header of each stream (title, artist, encoder,
// date, cover art, ...). It is rewritten in place to an empty comment list
// with a blank vendor string of the same total length, and the checksums of
// the affected pages are recomputed.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
)

var oggCRCTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		r := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(b []byte) uint32 {
	var c uint32
	for _, x := range b {
		c = c<<8 ^ oggCRCTable[byte(c>>24)^x]
	}
	return c
}

type oggSeg struct {
	off int64
	n   int
}

type oggPage struct {
	off int64
	n   int64
}

type oggStream struct {
	codec string
	pkt   int
	done  bool
	cur   []byte
	segs  []oggSeg
	pages []oggPage
}

func planOgg(r rd) (*plan, error) {
	p := &plan{}
	br := bufio.NewReaderSize(io.NewSectionReader(r.r, 0, r.size), 1<<20)
	streams := map[uint32]*oggStream{}
	dirty := map[int64]oggPage{}
	var pos int64
	pages := 0
	for {
		h := make([]byte, 27)
		if _, err := io.ReadFull(br, h); err != nil {
			break
		}
		if string(h[:4]) != "OggS" {
			if pages == 0 {
				return nil, errTrunc
			}
			break // damaged tail: left as is
		}
		lace := make([]byte, h[26])
		if _, err := io.ReadFull(br, lace); err != nil {
			break
		}
		dataLen := 0
		for _, l := range lace {
			dataLen += int(l)
		}
		page := oggPage{pos, int64(27 + len(lace) + dataLen)}
		serial := binary.LittleEndian.Uint32(h[14:])
		st := streams[serial]
		if st == nil {
			st = &oggStream{}
			streams[serial] = st
		}
		if st.done {
			if _, err := br.Discard(dataLen); err != nil {
				break
			}
		} else {
			data := make([]byte, dataLen)
			if _, err := io.ReadFull(br, data); err != nil {
				break
			}
			st.pages = append(st.pages, page)
			dp := 0
			for _, l := range lace {
				st.segs = append(st.segs, oggSeg{pos + 27 + int64(len(lace)) + int64(dp), int(l)})
				st.cur = append(st.cur, data[dp:dp+int(l)]...)
				dp += int(l)
				if l < 255 {
					oggPacket(st, p, dirty)
					if st.done {
						break
					}
				}
			}
		}
		pos += page.n
		pages++
	}

	// Recompute the checksum of every page we changed.
	for _, pg := range dirty {
		b, err := r.bytes(pg.off, pg.n)
		if err != nil {
			continue
		}
		for _, pt := range p.patches {
			lo, hi := pt.off, pt.off+pt.n
			if hi <= pg.off || lo >= pg.off+pg.n {
				continue
			}
			for k := lo; k < hi; k++ {
				if k >= pg.off && k < pg.off+pg.n {
					b[k-pg.off] = pt.data[k-lo]
				}
			}
		}
		b[22], b[23], b[24], b[25] = 0, 0, 0, 0
		c := make([]byte, 4)
		binary.LittleEndian.PutUint32(c, oggCRC(b))
		p.put(pg.off+22, c)
	}
	return p, nil
}

// oggPacket handles one completed packet of a stream.
func oggPacket(st *oggStream, p *plan, dirty map[int64]oggPage) {
	pkt, segs := st.cur, st.segs
	st.cur, st.segs = nil, nil
	n := st.pkt
	st.pkt++
	if n == 0 {
		switch {
		case bytes.HasPrefix(pkt, []byte("\x01vorbis")):
			st.codec = "vorbis"
		case bytes.HasPrefix(pkt, []byte("\x80theora")):
			st.codec = "theora"
		case bytes.HasPrefix(pkt, []byte("OpusHead")):
			st.codec = "opus"
		case bytes.HasPrefix(pkt, []byte("\x7fFLAC")):
			st.codec = "flac"
		case bytes.HasPrefix(pkt, []byte("Speex   ")):
			st.codec = "speex"
		default:
			st.done = true
		}
		st.pages = st.pages[len(st.pages)-1:]
		return
	}
	st.done = true
	prefix, framing := 0, false
	switch st.codec {
	case "vorbis":
		if !bytes.HasPrefix(pkt, []byte("\x03vorbis")) {
			return
		}
		prefix, framing = 7, true
	case "theora":
		if !bytes.HasPrefix(pkt, []byte("\x81theora")) {
			return
		}
		prefix = 7
	case "opus":
		if !bytes.HasPrefix(pkt, []byte("OpusTags")) {
			return
		}
		prefix = 8
	case "flac":
		if len(pkt) < 4 || pkt[0]&0x7F != 4 {
			return
		}
		prefix = 4
	case "speex":
	}
	body := pkt[prefix:]
	need := 8
	if framing {
		need = 9
	}
	if len(body) < need {
		return
	}
	vl := len(body) - need
	// Already clean (blank vendor, no comments)?
	le := binary.LittleEndian
	if int(le.Uint32(body)) == vl && allByte(body[4:4+vl], ' ') && le.Uint32(body[4+vl:]) == 0 {
		return
	}
	nb := make([]byte, len(body))
	le.PutUint32(nb, uint32(vl))
	for i := 4; i < 4+vl; i++ {
		nb[i] = ' '
	}
	if framing {
		nb[len(nb)-1] = 1
	}
	copy(pkt[prefix:], nb)

	// Map the rewritten packet bytes back onto their places in the file.
	k := 0
	for _, s := range segs {
		if s.n > 0 {
			p.put(s.off, append([]byte(nil), pkt[k:k+s.n]...))
		}
		k += s.n
		for _, pg := range st.pages {
			if s.off >= pg.off && s.off < pg.off+pg.n {
				dirty[pg.off] = pg
			}
		}
	}
	p.note("comment tags (title, artist, encoder, cover art)")
}
