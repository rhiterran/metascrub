// FLV.
//
// FLV metadata lives in script-data tags (onMetaData, onXMPData, ...). Text
// values there — encoder, creator, author, title, XMP — are blanked with
// spaces of the same length and dates are zeroed. Numeric values players use
// (duration, dimensions, frame rate, keyframe index) and codec IDs are kept.
package main

import (
	"encoding/binary"
	"errors"
	"strings"
)

type flvPlanner struct {
	rd
	p *plan
}

func planFLV(r rd) (*plan, error) {
	f := &flvPlanner{rd: r, p: &plan{}}
	h, err := f.bytes(0, 9)
	if err != nil {
		return nil, err
	}
	off := int64(binary.BigEndian.Uint32(h[5:])) + 4
	for off+11 <= f.size {
		th, err := f.bytes(off, 11)
		if err != nil {
			break
		}
		ds := int64(th[1])<<16 | int64(th[2])<<8 | int64(th[3])
		if off+11+ds > f.size {
			break
		}
		if th[0]&0x1F == 18 && ds <= 64<<20 {
			if data, err := f.bytes(off+11, ds); err == nil {
				a := &amf0{b: data, p: f.p}
				a.script()
				if a.changed {
					f.p.put(off+11, data)
				}
			}
		}
		off += 11 + ds + 4
	}
	return f.p, nil
}

type amf0 struct {
	b       []byte
	pos     int
	changed bool
	p       *plan
}

var errAMF = errors.New("bad AMF0 data")

func (a *amf0) script() {
	defer func() { recover() }() // malformed tail: keep what was done
	// The first value is the script name (e.g. "onMetaData"); keep it.
	if err := a.value("", false); err != nil {
		return
	}
	for a.pos < len(a.b) {
		if err := a.value("", true); err != nil {
			return
		}
	}
}

func (a *amf0) blankText(start, n int, key string, allowed bool) {
	if !allowed || strings.Contains(strings.ToLower(key), "codecid") {
		return
	}
	s := a.b[start : start+n]
	if allByte(s, ' ') {
		return
	}
	for i := range s {
		s[i] = ' '
	}
	a.changed = true
	a.p.note("encoder/creator tags")
}

func (a *amf0) value(key string, blank bool) error {
	if a.pos >= len(a.b) {
		return errAMF
	}
	t := a.b[a.pos]
	a.pos++
	be := binary.BigEndian
	switch t {
	case 0: // number
		a.pos += 8
	case 1: // boolean
		a.pos++
	case 2: // string
		n := int(be.Uint16(a.b[a.pos:]))
		a.blankText(a.pos+2, n, key, blank)
		a.pos += 2 + n
	case 3: // object
		return a.props(blank)
	case 5, 6, 13: // null, undefined, unsupported
	case 7: // reference
		a.pos += 2
	case 8: // ECMA array
		a.pos += 4
		return a.props(blank)
	case 10: // strict array
		n := int(be.Uint32(a.b[a.pos:]))
		a.pos += 4
		for i := 0; i < n; i++ {
			if err := a.value(key, blank); err != nil {
				return err
			}
		}
	case 11: // date
		d := a.b[a.pos : a.pos+10]
		if blank && !allZero(d) {
			for i := range d {
				d[i] = 0
			}
			a.changed = true
			a.p.note("creation date")
		}
		a.pos += 10
	case 12, 15: // long string, XML document
		n := int(be.Uint32(a.b[a.pos:]))
		a.blankText(a.pos+4, n, key, blank)
		if t == 15 && blank {
			a.p.note("XMP")
		}
		a.pos += 4 + n
	case 16: // typed object
		n := int(be.Uint16(a.b[a.pos:]))
		a.pos += 2 + n
		return a.props(blank)
	default:
		return errAMF
	}
	if a.pos > len(a.b) {
		return errAMF
	}
	return nil
}

func (a *amf0) props(blank bool) error {
	be := binary.BigEndian
	for {
		if a.pos+3 > len(a.b) {
			return errAMF
		}
		n := int(be.Uint16(a.b[a.pos:]))
		if n == 0 && a.b[a.pos+2] == 9 {
			a.pos += 3
			return nil
		}
		key := string(a.b[a.pos+2 : a.pos+2+n])
		a.pos += 2 + n
		if err := a.value(key, blank); err != nil {
			return err
		}
	}
}
