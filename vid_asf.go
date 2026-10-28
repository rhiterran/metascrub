// WMV / ASF.
//
// Removed: the content description (title, author, copyright, description),
// extended content description (WM/ tags, encoding software and time), content
// branding, markers and script commands — each becomes a same-size zero-filled
// Padding object — plus the file creation date and the values of descriptive
// entries in the metadata objects (entries players need, such as aspect
// ratio, are kept).
package main

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"unicode/utf16"
)

func asfGUID(s string) [16]byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		panic("bad GUID " + s)
	}
	var g [16]byte
	// first three fields are stored little-endian
	g[0], g[1], g[2], g[3] = b[3], b[2], b[1], b[0]
	g[4], g[5] = b[5], b[4]
	g[6], g[7] = b[7], b[6]
	copy(g[8:], b[8:])
	return g
}

var (
	guidASFHeader      = asfGUID("75B22630-668E-11CF-A6D9-00AA0062CE6C")
	guidFileProps      = asfGUID("8CABDCA1-A947-11CF-8EE4-00C00C205365")
	guidContentDesc    = asfGUID("75B22633-668E-11CF-A6D9-00AA0062CE6C")
	guidExtContentDesc = asfGUID("D2D0A440-E307-11D2-97F0-00A0C95EA850")
	guidBranding       = asfGUID("2211B3FA-BD23-11D2-B4B7-00A0C955FC6E")
	guidMarker         = asfGUID("F487CD01-A951-11CF-8EE6-00C00C205365")
	guidScriptCmd      = asfGUID("1EFB1A30-0B62-11D0-A39B-00A0C90348F6")
	guidHeaderExt      = asfGUID("5FBF03B5-A92E-11CF-8EE3-00C00C205365")
	guidMetadata       = asfGUID("C5F8CBEA-5BAF-4877-8467-AA8C44FA4CCA")
	guidMetadataLib    = asfGUID("44231C94-9498-49D1-A141-1D134E457054")
	guidPadding        = asfGUID("1806D474-CADF-4509-A4BA-9AABCB96AAE8")
)

// Metadata entries kept because they affect playback.
var asfKeepNames = map[string]bool{
	"AspectRatioX": true, "AspectRatioY": true, "IsVBR": true,
	"DeviceConformanceTemplate": true, "WM/StreamTypeInfo": true,
	"WM/VideoFrameRate": true,
}

type asfPlanner struct {
	rd
	p *plan
}

func planASF(r rd) (*plan, error) {
	a := &asfPlanner{rd: r, p: &plan{}}
	h, err := a.bytes(0, 30)
	if err != nil {
		return nil, err
	}
	hsize := int64(binary.LittleEndian.Uint64(h[16:]))
	if hsize < 30 || hsize > a.size {
		return nil, errTrunc
	}
	a.objects(30, hsize, false)
	return a.p, nil
}

func (a *asfPlanner) objects(start, end int64, inExt bool) {
	for off := start; off+24 <= end; {
		h, err := a.bytes(off, 24)
		if err != nil {
			return
		}
		var g [16]byte
		copy(g[:], h)
		size := int64(binary.LittleEndian.Uint64(h[16:]))
		if size < 24 || off+size > end {
			return
		}
		switch g {
		case guidContentDesc:
			a.pad(off, size, "title/author/copyright")
		case guidExtContentDesc:
			a.pad(off, size, "tags (encoder, software, dates)")
		case guidBranding:
			a.pad(off, size, "branding")
		case guidMarker:
			a.pad(off, size, "markers/chapters")
		case guidScriptCmd:
			a.pad(off, size, "script commands")
		case guidFileProps:
			if size >= 56 {
				if d, err := a.bytes(off+48, 8); err == nil && !allZero(d) {
					a.p.fillRange(off+48, 8, 0)
					a.p.note("creation date")
				}
			}
		case guidHeaderExt:
			if !inExt && size >= 46 {
				if d, err := a.bytes(off+42, 4); err == nil {
					ds := int64(binary.LittleEndian.Uint32(d))
					e := off + 46 + ds
					if e > off+size {
						e = off + size
					}
					a.objects(off+46, e, true)
				}
			}
		case guidMetadata, guidMetadataLib:
			a.metadataValues(off, size)
		}
		off += size
	}
}

func (a *asfPlanner) pad(off, size int64, label string) {
	a.p.put(off, guidPadding[:])
	a.p.fillRange(off+24, size-24, 0)
	a.p.note(label)
}

// metadataValues zeroes the values of descriptive Metadata / Metadata Library
// entries in place. Both objects share the record layout used here.
func (a *asfPlanner) metadataValues(off, size int64) {
	if size > 256<<20 || size < 26 {
		return
	}
	b, err := a.bytes(off, size)
	if err != nil {
		return
	}
	le := binary.LittleEndian
	count := int(le.Uint16(b[24:]))
	p := 26
	changed := false
	for i := 0; i < count && p+12 <= len(b); i++ {
		nameLen := int(le.Uint16(b[p+4:]))
		dataLen := int(le.Uint32(b[p+8:]))
		nameEnd := p + 12 + nameLen
		dataEnd := nameEnd + dataLen
		if dataEnd > len(b) || dataLen < 0 {
			break
		}
		name := utf16String(b[p+12 : nameEnd])
		if !asfKeepNames[name] && !allZero(b[nameEnd:dataEnd]) {
			for k := nameEnd; k < dataEnd; k++ {
				b[k] = 0
			}
			changed = true
		}
		p = dataEnd
	}
	if changed {
		a.p.put(off, b)
		a.p.note("tags (encoder, software, dates)")
	}
}

func utf16String(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}
