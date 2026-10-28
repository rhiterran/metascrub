// MPEG-TS / M2TS / MTS.
//
// Transport streams have no title/author/GPS fields. What they can carry is
// broadcast service information: channel and provider names (SDT), programme
// guide entries (EIT), and the broadcast date and time (TDT/TOT). Packets on
// those PIDs are turned into standard null packets in place, so the file keeps
// its exact size and packet timing; audio, video and subtitle packets are
// untouched.
package main

import (
	"bufio"
	"io"
	"os"
)

var tsInfoPID = map[int]bool{
	0x0011: true, // SDT/BAT: service and provider names
	0x0012: true, // EIT: programme guide
	0x0013: true, // RST
	0x0014: true, // TDT/TOT: broadcast date and time
	0x001E: true, // DIT
	0x001F: true, // SIT
	0x1FFB: true, // ATSC PSIP: channel names, programme guide
}

func copyTS(src, dst string, size int64, onFrac func(float64)) (int, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	head := make([]byte, 1024)
	hn, _ := io.ReadFull(in, head)
	head = head[:hn]
	pktSize, syncOff := 188, 0
	switch {
	case sniffSync(head, 4, 192):
		pktSize, syncOff = 192, 4
	case sniffSync(head, 0, 204):
		pktSize = 204
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}

	tmp := dst + ".partial"
	outF, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		if !ok {
			outF.Close()
			os.Remove(tmp)
		}
	}()
	br := bufio.NewReaderSize(in, 1<<20)
	bw := bufio.NewWriterSize(outF, 1<<20)
	pkt := make([]byte, pktSize)
	var pos, lastReport int64
	removed := 0
	for {
		n, rerr := io.ReadFull(br, pkt)
		if n < pktSize {
			if n > 0 {
				if _, err := bw.Write(pkt[:n]); err != nil {
					return 0, err
				}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			return 0, rerr
		}
		s := pkt[syncOff:]
		if s[0] != 0x47 {
			// Lost packet alignment: copy the rest unchanged.
			if _, err := bw.Write(pkt); err != nil {
				return 0, err
			}
			if _, err := io.Copy(bw, br); err != nil {
				return 0, err
			}
			break
		}
		pid := int(s[1]&0x1F)<<8 | int(s[2])
		if tsInfoPID[pid] {
			s[1], s[2], s[3] = 0x1F, 0xFF, 0x10
			for i := 4; i < 188 && i < len(s); i++ {
				s[i] = 0xFF
			}
			removed++
		}
		if _, err := bw.Write(pkt); err != nil {
			return 0, err
		}
		pos += int64(pktSize)
		if onFrac != nil && size > 0 && pos-lastReport >= 1<<20 {
			lastReport = pos
			onFrac(float64(pos) / float64(size))
		}
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	if err := outF.Close(); err != nil {
		return 0, err
	}
	os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return 0, err
	}
	ok = true
	if onFrac != nil {
		onFrac(1)
	}
	return removed, nil
}

func sniffSync(h []byte, first, step int) bool {
	for i := 0; i < 3; i++ {
		o := first + i*step
		if o >= len(h) || h[o] != 0x47 {
			return false
		}
	}
	return true
}
