// MKV / WebM (Matroska / EBML).
//
// Removed: the Tags and Chapters elements, the title, recording date and file
// names in the segment info, the muxing/writing application names, track
// names, and attached cover images (font attachments used by subtitles are
// kept). Removed elements become EBML Void elements of the same size; CRC-32
// elements are recomputed, and SeekHead entries that pointed at removed
// elements are voided too.
package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
)

const (
	ebmlHeader      = 0x1A45DFA3
	mkvSegment      = 0x18538067
	mkvSeekHead     = 0x114D9B74
	mkvSeek         = 0x4DBB
	mkvSeekID       = 0x53AB
	mkvInfo         = 0x1549A966
	mkvTracks       = 0x1654AE6B
	mkvTrackEntry   = 0xAE
	mkvName         = 0x536E
	mkvCluster      = 0x1F43B675
	mkvAttachments  = 0x1941A469
	mkvAttachedFile = 0x61A7
	mkvFileMime     = 0x4660
	mkvFileDesc     = 0x467E
	mkvChapters     = 0x1043A770
	mkvTags         = 0x1254C367
	mkvVoid         = 0xEC
	mkvCRC32        = 0xBF
	mkvTitle        = 0x7BA9
	mkvDateUTC      = 0x4461
	mkvMuxingApp    = 0x4D80
	mkvWritingApp   = 0x5741
	mkvSegFilename  = 0x7384
	mkvPrevFile     = 0x3C83B9
	mkvNextFile     = 0x3E83BB
)

type ebmlHead struct {
	id      uint64
	hdr     int64
	size    int64
	unknown bool
}

// parseEBML decodes an element ID and size from b.
func parseEBML(b []byte) (ebmlHead, bool) {
	if len(b) < 2 || b[0] == 0 {
		return ebmlHead{}, false
	}
	idLen := 1
	for b[0]&(0x80>>(idLen-1)) == 0 {
		idLen++
	}
	if idLen > 4 || len(b) < idLen+1 {
		return ebmlHead{}, false
	}
	var id uint64
	for i := 0; i < idLen; i++ {
		id = id<<8 | uint64(b[i])
	}
	s := b[idLen:]
	if s[0] == 0 {
		return ebmlHead{}, false
	}
	sl := 1
	for s[0]&(0x80>>(sl-1)) == 0 {
		sl++
	}
	if len(s) < sl {
		return ebmlHead{}, false
	}
	v := uint64(s[0] & (0xFF >> sl))
	allOnes := v == uint64(0xFF>>sl)
	for i := 1; i < sl; i++ {
		v = v<<8 | uint64(s[i])
		if s[i] != 0xFF {
			allOnes = false
		}
	}
	return ebmlHead{id: id, hdr: int64(idLen + sl), size: int64(v), unknown: allOnes}, true
}

type mkvElem struct {
	id         uint64
	start, hdr int
	size       int
}

func (e mkvElem) data(b []byte) []byte { return b[e.start+e.hdr : e.start+e.hdr+e.size] }
func (e mkvElem) total() int           { return e.hdr + e.size }

// mkvChildren lists the child elements in an in-memory master element body.
func mkvChildren(b []byte) []mkvElem {
	var out []mkvElem
	for p := 0; p < len(b); {
		h, ok := parseEBML(b[p:])
		if !ok || h.unknown || p+int(h.hdr)+int(h.size) > len(b) || h.size < 0 {
			break
		}
		out = append(out, mkvElem{id: h.id, start: p, hdr: int(h.hdr), size: int(h.size)})
		p += int(h.hdr) + int(h.size)
	}
	return out
}

// voidBytes overwrites total bytes at b[0:] with a zero-filled Void element.
func voidBytes(b []byte, total int) {
	if total < 2 {
		return
	}
	b[0] = mkvVoid
	rem := total - 1
	w := rem
	if w > 8 {
		w = 8
	}
	val := uint64(rem - w)
	for i := 1; i <= w; i++ {
		b[i] = 0
	}
	// width-w vint: marker bit then the value big-endian
	for i := w; i >= 1; i-- {
		b[i] = byte(val)
		val >>= 8
	}
	b[1] |= 0x80 >> (w - 1)
	for i := 1 + w; i < total; i++ {
		b[i] = 0
	}
}

// fixCRC recomputes a leading CRC-32 child of a master element body.
func fixCRC(b []byte) {
	ch := mkvChildren(b)
	if len(ch) == 0 || ch[0].id != mkvCRC32 || ch[0].size != 4 {
		return
	}
	d := ch[0].data(b)
	binary.LittleEndian.PutUint32(d, crc32.ChecksumIEEE(b[ch[0].start+ch[0].total():]))
}

type mkvPlanner struct {
	rd
	p *plan
}

func planMKV(r rd) (*plan, error) {
	k := &mkvPlanner{rd: r, p: &plan{}}
	h, err := k.head(0)
	if err != nil || h.id != ebmlHeader || h.unknown {
		return nil, errTrunc
	}
	off := h.hdr + h.size
	segs := 0
	for off < k.size {
		h, err := k.head(off)
		if err != nil {
			break
		}
		if h.id != mkvSegment {
			if h.unknown {
				break
			}
			off += h.hdr + h.size
			continue
		}
		segs++
		start := off + h.hdr
		end := k.size
		if !h.unknown && start+h.size < end {
			end = start + h.size
		}
		if err := k.segment(start, end); err != nil {
			return nil, err
		}
		off = end
	}
	if segs == 0 {
		return nil, errTrunc
	}
	return k.p, nil
}

func (k *mkvPlanner) head(off int64) (ebmlHead, error) {
	n := int64(12)
	if k.size-off < n {
		n = k.size - off
	}
	b, err := k.bytes(off, n)
	if err != nil {
		return ebmlHead{}, err
	}
	h, ok := parseEBML(b)
	if !ok {
		return ebmlHead{}, errTrunc
	}
	return h, nil
}

type mkvTop struct {
	off int64
	h   ebmlHead
}

func (k *mkvPlanner) segment(start, end int64) error {
	var tops []mkvTop
	for off := start; off < end; {
		h, err := k.head(off)
		if err != nil {
			break
		}
		if h.unknown {
			if h.id == mkvCluster {
				off = k.skipUnknownCluster(off+h.hdr, end)
				continue
			}
			break
		}
		if off+h.hdr+h.size > end {
			break // truncated final element
		}
		tops = append(tops, mkvTop{off, h})
		off += h.hdr + h.size
	}

	voided := map[uint64]bool{}
	for _, t := range tops {
		switch t.h.id {
		case mkvTags:
			k.voidFile(t, "tags")
			voided[mkvTags] = true
		case mkvChapters:
			k.voidFile(t, "chapters")
			voided[mkvChapters] = true
		case mkvInfo, mkvTracks, mkvAttachments:
			if k.rewrite(t, nil) {
				voided[mkvAttachments] = true
			}
		}
	}
	for _, t := range tops {
		if t.h.id == mkvSeekHead {
			k.rewrite(t, voided)
		}
	}
	return nil
}

func (k *mkvPlanner) voidFile(t mkvTop, label string) {
	// Void header for the full length, then zeros for the rest.
	total := t.h.hdr + t.h.size
	if total <= 9 {
		buf := make([]byte, total)
		voidBytes(buf, int(total))
		k.p.put(t.off, buf)
	} else {
		hdr := make([]byte, 9)
		hdr[0] = mkvVoid
		val := uint64(total - 9)
		for i := 8; i >= 1; i-- {
			hdr[i] = byte(val)
			val >>= 8
		}
		hdr[1] |= 0x01
		k.p.put(t.off, hdr)
		k.p.fillRange(t.off+9, total-9, 0)
	}
	k.p.note(label)
}

func (k *mkvPlanner) skipUnknownCluster(off, end int64) int64 {
	for off < end {
		h, err := k.head(off)
		if err != nil || h.unknown {
			return end
		}
		switch h.id {
		case 0xE7, 0xA7, 0xAB, 0xA3, 0xA0, 0xAF, 0x5854, mkvVoid, mkvCRC32:
			off += h.hdr + h.size
		default:
			return off
		}
	}
	return end
}

// rewrite loads a small top-level master element, edits it in memory, and
// emits the whole element as one patch if it changed. For Attachments it
// returns true when every attachment was removed (the element was voided).
func (k *mkvPlanner) rewrite(t mkvTop, voided map[uint64]bool) bool {
	total := t.h.hdr + t.h.size
	if total > 512<<20 {
		if t.h.id == mkvAttachments {
			k.voidFile(t, "attachments")
			return true
		}
		return false
	}
	orig, err := k.bytes(t.off, total)
	if err != nil {
		return false
	}
	buf := append([]byte(nil), orig...)
	body := buf[t.h.hdr:]
	allGone := false

	switch t.h.id {
	case mkvInfo:
		for _, c := range mkvChildren(body) {
			switch c.id {
			case mkvTitle:
				voidBytes(body[c.start:], c.total())
				k.p.note("title")
			case mkvDateUTC:
				voidBytes(body[c.start:], c.total())
				k.p.note("recording date")
			case mkvSegFilename, mkvPrevFile, mkvNextFile:
				voidBytes(body[c.start:], c.total())
				k.p.note("file names")
			case mkvMuxingApp, mkvWritingApp:
				if d := c.data(body); !allZero(d) {
					for i := range d {
						d[i] = 0
					}
					k.p.note("encoder names")
				}
			}
		}
	case mkvTracks:
		for _, c := range mkvChildren(body) {
			if c.id != mkvTrackEntry {
				continue
			}
			te := c.data(body)
			for _, tc := range mkvChildren(te) {
				if tc.id == mkvName {
					voidBytes(te[tc.start:], tc.total())
					k.p.note("track names")
				}
			}
			fixCRC(te)
		}
	case mkvAttachments:
		files := mkvChildren(body)
		removed, count := 0, 0
		for _, c := range files {
			if c.id != mkvAttachedFile {
				continue
			}
			count++
			af := c.data(body)
			mime := ""
			for _, ac := range mkvChildren(af) {
				if ac.id == mkvFileMime {
					mime = strings.ToLower(string(bytes.TrimRight(ac.data(af), "\x00")))
				}
			}
			if strings.HasPrefix(mime, "image/") {
				voidBytes(body[c.start:], c.total())
				removed++
				k.p.note("cover art")
				continue
			}
			for _, ac := range mkvChildren(af) {
				if ac.id == mkvFileDesc {
					voidBytes(af[ac.start:], ac.total())
					k.p.note("attachment descriptions")
				}
			}
			fixCRC(af)
		}
		if count > 0 && removed == count {
			allGone = true
			k.voidFile(t, "cover art")
			return true
		}
	case mkvSeekHead:
		for _, c := range mkvChildren(body) {
			if c.id != mkvSeek {
				continue
			}
			for _, sc := range mkvChildren(c.data(body)) {
				if sc.id != mkvSeekID {
					continue
				}
				var id uint64
				for _, x := range sc.data(c.data(body)) {
					id = id<<8 | uint64(x)
				}
				if voided[id] {
					voidBytes(body[c.start:], c.total())
				}
				break
			}
		}
	}
	fixCRC(body)
	if !bytes.Equal(buf, orig) {
		k.p.put(t.off, buf)
	}
	return allGone
}
