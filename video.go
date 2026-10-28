// Native, lossless video metadata removal.
//
// Every supported container is handled the same way: the file structure is
// parsed (reading only headers, seeking past the audio/video payload), and the
// metadata found is turned into a list of same-size "patches" — a metadata box
// becomes padding of the same length, a creation date becomes zero, a tag
// value becomes blank. The original is then streamed to the copy with those
// patches applied. Because nothing changes size, every offset, index and
// audio/video byte in the file stays exactly where it was: the streams in the
// copy are byte-identical to the original and nothing is re-muxed.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var errTrunc = errors.New("truncated or damaged file structure")

// patch replaces bytes at off in the copy: with data, or (data == nil) with
// n copies of fill.
type patch struct {
	off  int64
	n    int64
	data []byte
	fill byte
}

// plan is what a container parser produces: the patches to apply, plus short
// human-readable labels of what was removed (for the log).
type plan struct {
	patches []patch
	removed []string
}

func (p *plan) put(off int64, b []byte) {
	if len(b) > 0 {
		p.patches = append(p.patches, patch{off: off, n: int64(len(b)), data: b})
	}
}

func (p *plan) fillRange(off, n int64, b byte) {
	if n > 0 {
		p.patches = append(p.patches, patch{off: off, n: n, fill: b})
	}
}

func (p *plan) note(label string) {
	for _, l := range p.removed {
		if l == label {
			return
		}
	}
	p.removed = append(p.removed, label)
}

// rd reads bounded ranges from a file.
type rd struct {
	r    io.ReaderAt
	size int64
}

func (x rd) bytes(off int64, n int64) ([]byte, error) {
	if off < 0 || n < 0 || off+n > x.size {
		return nil, errTrunc
	}
	b := make([]byte, n)
	if _, err := x.r.ReadAt(b, off); err != nil && !(errors.Is(err, io.EOF) && n == 0) {
		if errors.Is(err, io.EOF) {
			return nil, errTrunc
		}
		return nil, err
	}
	return b, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func allByte(b []byte, v byte) bool {
	for _, c := range b {
		if c != v {
			return false
		}
	}
	return true
}

// ---- format detection ----

func sniffVideo(h []byte) string {
	has := func(off int, s string) bool {
		return len(h) >= off+len(s) && string(h[off:off+len(s)]) == s
	}
	sync := func(offs ...int) bool {
		for _, o := range offs {
			if len(h) <= o || h[o] != 0x47 {
				return false
			}
		}
		return true
	}
	switch {
	case len(h) >= 4 && h[0] == 0x1A && h[1] == 0x45 && h[2] == 0xDF && h[3] == 0xA3:
		return "mkv"
	case has(0, "RIFF") && has(8, "AVI "):
		return "avi"
	case len(h) >= 16 && bytes.Equal(h[:16], guidASFHeader[:]):
		return "asf"
	case has(0, "FLV") && len(h) > 3 && h[3] == 1:
		return "flv"
	case has(0, "OggS"):
		return "ogg"
	case sync(0, 188, 376):
		return "ts"
	case sync(4, 196, 388):
		return "ts"
	case sync(0, 204, 408):
		return "ts"
	case len(h) >= 4 && h[0] == 0 && h[1] == 0 && h[2] == 1 && h[3] == 0xBA:
		return "ps"
	}
	if len(h) >= 8 {
		switch string(h[4:8]) {
		case "ftyp", "moov", "mdat", "free", "skip", "wide", "pnot", "uuid", "PICT":
			return "mp4"
		}
	}
	return ""
}

// scrubVideoFile writes a metadata-free copy of src to dst. It returns a short
// description of what was removed, for the log.
func scrubVideoFile(src, dst, ffmpeg, ffprobe string, onFrac func(float64)) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return "", err
	}
	size := st.Size()
	head := make([]byte, 1024)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	format := sniffVideo(head)

	var pl *plan
	var perr error
	r := rd{f, size}
	switch format {
	case "mp4":
		pl, perr = planMP4(r)
	case "mkv":
		pl, perr = planMKV(r)
	case "avi":
		pl, perr = planAVI(r)
	case "asf":
		pl, perr = planASF(r)
	case "flv":
		pl, perr = planFLV(r)
	case "ogg":
		pl, perr = planOgg(r)
	case "ps":
		pl = &plan{}
	case "ts":
		f.Close()
		removed, err := copyTS(src, dst, size, onFrac)
		if err != nil {
			return "", err
		}
		if removed > 0 {
			return "removed: broadcast service info (channel/programme names, broadcast time)", nil
		}
		return "no container metadata present", nil
	default:
		perr = errors.New("unrecognised video structure")
	}
	f.Close()

	if perr != nil || pl == nil {
		if ffmpeg == "" {
			return "", perr
		}
		if err := stripVideoProgress(src, dst, ffmpeg, ffprobe, onFrac); err != nil {
			return "", fmt.Errorf("%v; compatibility mode also failed: %v", perr, err)
		}
		return "compatibility mode", nil
	}

	if err := copyPatched(src, dst, size, pl.patches, onFrac); err != nil {
		return "", err
	}
	if len(pl.removed) == 0 {
		return "no metadata present", nil
	}
	return "removed: " + strings.Join(pl.removed, ", "), nil
}

// copyPatched streams src to dst, applying patches. The output is written to a
// temporary name first and renamed only when complete.
func copyPatched(src, dst string, size int64, patches []patch, onFrac func(float64)) error {
	sort.SliceStable(patches, func(i, j int) bool { return patches[i].off < patches[j].off })

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(tmp)
		}
	}()

	buf := make([]byte, 1<<20)
	var pos int64
	first := 0
	for {
		n, rerr := io.ReadFull(in, buf)
		if n > 0 {
			chunk := buf[:n]
			end := pos + int64(n)
			for first < len(patches) && patches[first].off+patches[first].n <= pos {
				first++
			}
			for j := first; j < len(patches) && patches[j].off < end; j++ {
				p := patches[j]
				lo, hi := p.off, p.off+p.n
				if hi <= pos {
					continue
				}
				if lo < pos {
					lo = pos
				}
				if hi > end {
					hi = end
				}
				dstB := chunk[lo-pos : hi-pos]
				if p.data != nil {
					copy(dstB, p.data[lo-p.off:hi-p.off])
				} else {
					for k := range dstB {
						dstB[k] = p.fill
					}
				}
			}
			if _, err := out.Write(chunk); err != nil {
				return err
			}
			pos = end
			if onFrac != nil && size > 0 {
				onFrac(float64(pos) / float64(size))
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if pos != size {
		return fmt.Errorf("source changed while copying (%d of %d bytes)", pos, size)
	}
	if err := out.Close(); err != nil {
		return err
	}
	os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	ok = true
	return nil
}
