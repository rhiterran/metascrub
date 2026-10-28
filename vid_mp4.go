// MP4 / MOV / M4V / 3GP / 3G2 / F4V (ISO base media file format and QuickTime).
//
// Removed: user data and metadata boxes (title, artist, comments, GPS location,
// Apple/Android/Samsung keys, cover art, chapters), XMP and vendor uuid boxes,
// creation/modification dates, handler names, encoder and device names in the
// sample descriptions, dashcam GPS logs, and the samples of telemetry tracks
// (GoPro/DJI/Google GPS, Sony real-time metadata, time-of-day timecode).
// Removed boxes become "free" padding of the same size.
package main

import "encoding/binary"

var xmpUUID = []byte{0xBE, 0x7A, 0xCF, 0xCB, 0x97, 0xA9, 0x42, 0xE8, 0x9C, 0x71, 0x99, 0x94, 0x91, 0xE3, 0xAF, 0xAC}

// Sample-entry formats of tracks whose samples are metadata only.
var telemetryFormats = map[string]string{
	"gpmd": "GoPro GPS/telemetry track",
	"camm": "camera motion/GPS track",
	"djmd": "DJI telemetry track",
	"dbgi": "DJI telemetry track",
	"rtmd": "Sony real-time metadata track",
	"tmcd": "time-of-day timecode",
	"mett": "timed metadata track",
	"metx": "timed metadata track",
}

type mp4Box struct {
	off, size, hdr int64
	typ            string
}

func (b mp4Box) payload() int64 { return b.off + b.hdr }
func (b mp4Box) end() int64     { return b.off + b.size }

type mp4Planner struct {
	rd
	p *plan
	// Tracks whose samples are metadata (chapter titles, telemetry), by
	// track ID, with the label to report. Used for fragmented files too.
	zeroTracks map[uint32]string
	trexSize   map[uint32]uint32
}

func planMP4(r rd) (*plan, error) {
	m := &mp4Planner{rd: r, p: &plan{}, zeroTracks: map[uint32]string{}, trexSize: map[uint32]uint32{}}
	var off int64
	sawMoov := false
	for off+8 <= m.size {
		bx, err := m.box(off, m.size)
		if err != nil {
			if off == 0 {
				return nil, err
			}
			break // damaged tail (e.g. interrupted recording): left as is
		}
		switch bx.typ {
		case "moov":
			sawMoov = true
			if err := m.moov(bx); err != nil {
				return nil, err
			}
		case "uuid":
			m.blankUUID(bx, true)
		case "meta", "udta":
			m.blank(bx, "metadata tags")
		case "gps ":
			m.novatekGPS(bx)
		case "moof":
			if sawMoov && len(m.zeroTracks) > 0 {
				m.moof(bx)
			}
		}
		off += bx.size
	}
	if !sawMoov {
		return nil, errTrunc
	}
	return m.p, nil
}

func (m *mp4Planner) box(off, end int64) (mp4Box, error) {
	if end-off < 8 {
		return mp4Box{}, errTrunc
	}
	h, err := m.bytes(off, 8)
	if err != nil {
		return mp4Box{}, err
	}
	size := int64(binary.BigEndian.Uint32(h))
	hdr := int64(8)
	switch size {
	case 1:
		l, err := m.bytes(off+8, 8)
		if err != nil {
			return mp4Box{}, err
		}
		size = int64(binary.BigEndian.Uint64(l))
		hdr = 16
	case 0:
		size = end - off
	}
	bx := mp4Box{off: off, size: size, hdr: hdr, typ: string(h[4:8])}
	if size < hdr || off+size > end || size < 0 {
		return bx, errTrunc
	}
	return bx, nil
}

func (m *mp4Planner) children(parent mp4Box, skip int64, fn func(mp4Box) error) error {
	off, end := parent.payload()+skip, parent.end()
	for off+8 <= end {
		bx, err := m.box(off, end)
		if err != nil {
			return nil // tolerate trailing junk inside a container
		}
		if err := fn(bx); err != nil {
			return err
		}
		off += bx.size
	}
	return nil
}

// blank turns a box into same-size zero-filled "free" padding.
func (m *mp4Planner) blank(bx mp4Box, label string) {
	m.p.put(bx.off+4, []byte("free"))
	m.p.fillRange(bx.payload(), bx.size-bx.hdr, 0)
	m.p.note(label)
}

func (m *mp4Planner) blankUUID(bx mp4Box, vendorToo bool) {
	id, err := m.bytes(bx.payload(), 16)
	if err == nil && string(id) == string(xmpUUID) {
		m.blank(bx, "XMP")
	} else if vendorToo {
		m.blank(bx, "vendor metadata")
	}
}

// zeroTimes clears creation/modification dates in mvhd/tkhd/mdhd.
func (m *mp4Planner) zeroTimes(bx mp4Box) {
	v, err := m.bytes(bx.payload(), 1)
	if err != nil {
		return
	}
	n := int64(8)
	if v[0] == 1 {
		n = 16
	}
	if bx.size-bx.hdr < 4+n {
		return
	}
	if t, err := m.bytes(bx.payload()+4, n); err == nil && !allZero(t) {
		m.p.fillRange(bx.payload()+4, n, 0)
		m.p.note("creation dates")
	}
}

// zeroHdlrName clears the free-text name in a handler box ("GoPro AVC", ...).
func (m *mp4Planner) zeroHdlrName(bx mp4Box) {
	start := bx.payload() + 24
	if start >= bx.end() {
		return
	}
	if name, err := m.bytes(start, bx.end()-start); err == nil && !allZero(name) {
		m.p.fillRange(start, bx.end()-start, 0)
		m.p.note("handler/device names")
	}
}

func (m *mp4Planner) moov(moov mp4Box) error {
	var traks []*trakInfo
	err := m.children(moov, 0, func(bx mp4Box) error {
		switch bx.typ {
		case "mvhd":
			m.zeroTimes(bx)
		case "udta", "meta":
			m.blank(bx, "metadata tags")
		case "uuid":
			m.blankUUID(bx, false)
		case "gps ":
			m.novatekGPS(bx)
		case "mvex":
			_ = m.children(bx, 0, func(x mp4Box) error {
				if x.typ == "trex" {
					if b, err := m.bytes(x.payload(), 20); err == nil {
						m.trexSize[binary.BigEndian.Uint32(b[4:])] = binary.BigEndian.Uint32(b[16:])
					}
				}
				return nil
			})
		case "trak":
			t, err := m.trak(bx)
			if t != nil {
				traks = append(traks, t)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	// QuickTime-style chapters are a text track referenced by another track's
	// "chap" reference; its samples hold the chapter titles. Blank them (the
	// chapter positions remain, unnamed).
	chap := map[uint32]bool{}
	for _, t := range traks {
		for _, id := range t.chapRefs {
			chap[id] = true
		}
	}
	for _, t := range traks {
		if t.telemetry != "" {
			m.zeroTracks[t.id] = t.telemetry
		}
		if chap[t.id] {
			m.zeroTracks[t.id] = "chapter names"
			if t.haveStsz && t.haveStsc && t.haveStco && m.zeroSamples(t) {
				m.p.note("chapter names")
			}
		}
	}
	return nil
}

// moof zeroes, inside a movie fragment, the samples of metadata-only tracks.
func (m *mp4Planner) moof(moof mp4Box) {
	be := binary.BigEndian
	var prevEnd int64 = -1
	_ = m.children(moof, 0, func(traf mp4Box) error {
		if traf.typ != "traf" {
			return nil
		}
		var id uint32
		var label string
		var target bool
		base := moof.off
		var defSize uint32
		cursor := int64(-1)
		return m.children(traf, 0, func(bx mp4Box) error {
			n := bx.size - bx.hdr
			if n > 64<<20 {
				return nil
			}
			b, err := m.bytes(bx.payload(), n)
			if err != nil || len(b) < 8 {
				return nil
			}
			flags := uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
			switch bx.typ {
			case "tfhd":
				id = be.Uint32(b[4:])
				label, target = m.zeroTracks[id]
				defSize = m.trexSize[id]
				p := 8
				if flags&0x1 != 0 && p+8 <= len(b) {
					base = int64(be.Uint64(b[p:]))
					p += 8
				} else if flags&0x20000 == 0 && prevEnd >= 0 {
					base = prevEnd
				}
				if flags&0x2 != 0 {
					p += 4
				}
				if flags&0x8 != 0 {
					p += 4
				}
				if flags&0x10 != 0 && p+4 <= len(b) {
					defSize = be.Uint32(b[p:])
				}
				cursor = base
			case "trun":
				if cursor < 0 {
					return nil
				}
				count := int(be.Uint32(b[4:]))
				p := 8
				if flags&0x1 != 0 && p+4 <= len(b) {
					cursor = base + int64(int32(be.Uint32(b[p:])))
					p += 4
				}
				if flags&0x4 != 0 {
					p += 4
				}
				for i := 0; i < count; i++ {
					size := int64(defSize)
					if flags&0x100 != 0 {
						p += 4
					}
					if flags&0x200 != 0 {
						if p+4 > len(b) {
							return nil
						}
						size = int64(be.Uint32(b[p:]))
						p += 4
					}
					if flags&0x400 != 0 {
						p += 4
					}
					if flags&0x800 != 0 {
						p += 4
					}
					if target && size > 0 && cursor >= 0 && cursor+size <= m.size {
						if d, err := m.bytes(cursor, size); err == nil && !allZero(d) {
							m.p.fillRange(cursor, size, 0)
							m.p.note(label)
						}
					}
					cursor += size
				}
				prevEnd = cursor
			}
			return nil
		})
	})
}

type trakInfo struct {
	id                 uint32
	chapRefs           []uint32
	telemetry          string
	handler            string
	stsd, stsz, stsc   mp4Box
	stco               mp4Box
	co64               bool
	haveStsz, haveStco bool
	haveStsc, haveStsd bool
}

func (m *mp4Planner) trak(trak mp4Box) (*trakInfo, error) {
	t := &trakInfo{}
	var walk func(parent mp4Box) error
	walk = func(parent mp4Box) error {
		return m.children(parent, 0, func(bx mp4Box) error {
			switch bx.typ {
			case "tkhd", "mdhd":
				m.zeroTimes(bx)
				if bx.typ == "tkhd" {
					if v, err := m.bytes(bx.payload(), 1); err == nil {
						at := int64(12)
						if v[0] == 1 {
							at = 20
						}
						if b, err := m.bytes(bx.payload()+at, 4); err == nil {
							t.id = binary.BigEndian.Uint32(b)
						}
					}
				}
			case "tref":
				_ = m.children(bx, 0, func(r mp4Box) error {
					if r.typ == "chap" && r.size-r.hdr <= 1<<16 {
						if b, err := m.bytes(r.payload(), r.size-r.hdr); err == nil {
							for i := 0; i+4 <= len(b); i += 4 {
								t.chapRefs = append(t.chapRefs, binary.BigEndian.Uint32(b[i:]))
							}
						}
					}
					return nil
				})
			case "udta", "meta":
				m.blank(bx, "metadata tags")
			case "mdia", "minf", "stbl":
				return walk(bx)
			case "hdlr":
				if parent.typ == "mdia" {
					if h, err := m.bytes(bx.payload()+8, 4); err == nil {
						t.handler = string(h)
					}
				}
				m.zeroHdlrName(bx)
			case "stsd":
				t.stsd, t.haveStsd = bx, true
			case "stsz":
				t.stsz, t.haveStsz = bx, true
			case "stsc":
				t.stsc, t.haveStsc = bx, true
			case "stco", "co64":
				t.stco, t.haveStco, t.co64 = bx, true, bx.typ == "co64"
			}
			return nil
		})
	}
	if err := walk(trak); err != nil {
		return nil, err
	}
	if !t.haveStsd {
		return t, nil
	}

	telemetry := ""
	if t.handler == "tmcd" {
		telemetry = telemetryFormats["tmcd"]
	}
	_ = m.children(t.stsd, 8, func(e mp4Box) error {
		if l, ok := telemetryFormats[e.typ]; ok {
			telemetry = l
		}
		// QuickTime vendor code (bytes 12..16 of an audio/video sample entry)
		// and the encoder/device "compressor name" of video entries.
		switch t.handler {
		case "vide":
			m.zeroField(e, 12, 4, "encoder/device names")
			m.zeroField(e, 42, 32, "encoder/device names")
		case "soun":
			m.zeroField(e, 12, 4, "encoder/device names")
		}
		return nil
	})
	t.telemetry = telemetry
	if telemetry != "" && t.haveStsz && t.haveStsc && t.haveStco {
		if m.zeroSamples(t) {
			m.p.note(telemetry)
		}
	}
	return t, nil
}

func (m *mp4Planner) zeroField(e mp4Box, at, n int64, label string) {
	start := e.payload() + at
	if start+n > e.end() {
		return
	}
	if b, err := m.bytes(start, n); err == nil && !allZero(b) {
		m.p.fillRange(start, n, 0)
		m.p.note(label)
	}
}

// zeroSamples zero-fills every sample of a metadata-only track, located via
// its sample-size, sample-to-chunk and chunk-offset tables.
func (m *mp4Planner) zeroSamples(t *trakInfo) bool {
	const maxTable = 64 << 20
	read := func(bx mp4Box) []byte {
		if bx.size-bx.hdr > maxTable {
			return nil
		}
		b, _ := m.bytes(bx.payload(), bx.size-bx.hdr)
		return b
	}
	stsz, stsc, stco := read(t.stsz), read(t.stsc), read(t.stco)
	if len(stsz) < 12 || len(stsc) < 8 || len(stco) < 8 {
		return false
	}
	be := binary.BigEndian
	fixed := int64(be.Uint32(stsz[4:]))
	count := int(be.Uint32(stsz[8:]))
	sizeOf := func(i int) int64 {
		if fixed != 0 {
			return fixed
		}
		p := 12 + 4*i
		if p+4 > len(stsz) {
			return 0
		}
		return int64(be.Uint32(stsz[p:]))
	}
	type run struct{ first, spc int }
	var runs []run
	nr := int(be.Uint32(stsc[4:]))
	for i := 0; i < nr && 8+12*i+12 <= len(stsc); i++ {
		p := 8 + 12*i
		runs = append(runs, run{int(be.Uint32(stsc[p:])), int(be.Uint32(stsc[p+4:]))})
	}
	if len(runs) == 0 {
		return false
	}
	nc := int(be.Uint32(stco[4:]))
	w := 4
	if t.co64 {
		w = 8
	}
	any := false
	sample, ri := 0, 0
	for c := 0; c < nc && 8+w*c+w <= len(stco) && sample < count; c++ {
		var off int64
		if t.co64 {
			off = int64(be.Uint64(stco[8+8*c:]))
		} else {
			off = int64(be.Uint32(stco[8+4*c:]))
		}
		for ri+1 < len(runs) && runs[ri+1].first <= c+1 {
			ri++
		}
		var n int64
		for k := 0; k < runs[ri].spc && sample < count; k++ {
			n += sizeOf(sample)
			sample++
		}
		if n > 0 && off >= 0 && off+n <= m.size {
			if n <= 16<<20 {
				if b, err := m.bytes(off, n); err == nil && allZero(b) {
					continue // already blank
				}
			}
			m.p.fillRange(off, n, 0)
			any = true
		}
	}
	return any
}

// novatekGPS handles the "gps " index box written by Novatek-based dashcams:
// it lists free boxes elsewhere in the file that hold the GPS log. Each
// listed box is zeroed (after checking it really is a free box), and the
// index itself is blanked.
func (m *mp4Planner) novatekGPS(bx mp4Box) {
	defer m.blank(bx, "dashcam GPS log")
	n := bx.size - bx.hdr
	if n < 8 || n > 16<<20 {
		return
	}
	b, err := m.bytes(bx.payload(), n)
	if err != nil {
		return
	}
	be := binary.BigEndian
	count := int(be.Uint32(b[4:]))
	for i := 0; i < count && 8+8*i+8 <= len(b); i++ {
		off := int64(be.Uint32(b[8+8*i:]))
		if off+8 > m.size {
			continue
		}
		h, err := m.bytes(off, 8)
		if err != nil || string(h[4:8]) != "free" {
			continue
		}
		sz := int64(be.Uint32(h))
		if sz >= 8 && off+sz <= m.size {
			m.p.fillRange(off+8, sz-8, 0)
		}
	}
}
