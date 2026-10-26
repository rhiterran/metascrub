// metascrub core: find image and video files and write copies with all
// metadata removed. Images are stripped losslessly in pure Go; videos are
// remuxed losslessly via ffmpeg (stream copy). Originals are never modified.
//
// Work is reported through a Progress struct so a GUI can show a live bar and
// the current file, and so the caller can run it on a background thread.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true}

var videoExt = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true,
	".avi": true, ".wmv": true, ".flv": true, ".mpg": true, ".mpeg": true,
	".m2ts": true, ".mts": true, ".ts": true, ".3gp": true, ".3g2": true,
	".ogv": true, ".vob": true, ".asf": true, ".f4v": true,
}

func kind(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case imageExt[ext]:
		return "image"
	case videoExt[ext]:
		return "video"
	}
	return ""
}

// Progress carries callbacks invoked during a run. They may be called from a
// background goroutine, so a GUI implementation must not touch the UI directly
// here — it should stash the values and let the UI thread read them.
type Progress struct {
	// Log receives one human-readable line per file (and warnings).
	Log func(string)
	// Status reports overall progress: frac in [0,1], 1-based file index, total
	// file count, and the current file's name.
	Status func(frac float64, idx, total int, name string)
}

func (p Progress) log(s string) {
	if p.Log != nil {
		p.Log(s)
	}
}
func (p Progress) status(frac float64, idx, total int, name string) {
	if p.Status != nil {
		p.Status(frac, idx, total, name)
	}
}

// Result summarizes a run.
type Result struct {
	Processed       int
	Failed          int
	SkippedNoFFmpeg int
	OutLoc          string
}

type fileItem struct {
	src, dst, label, kind string
	size                  int64
}

// collect builds the list of files to process and the output location.
func collect(path string, recursive bool) (items []fileItem, outLoc string, err error) {
	info, e := os.Stat(path)
	if e != nil {
		return nil, "", e
	}

	if !info.IsDir() {
		if kind(path) == "" {
			return nil, "", fmt.Errorf("unsupported file type: %s", filepath.Ext(path))
		}
		dst := withSuffix(path, "-nometa")
		return []fileItem{{src: path, dst: dst, label: filepath.Base(path), kind: kind(path), size: info.Size()}}, dst, nil
	}

	in := mustAbs(path)
	outDir := filepath.Join(filepath.Dir(in), filepath.Base(in)+"-cleaned")
	if outDir == in {
		return nil, "", fmt.Errorf("cannot compute a distinct output folder")
	}
	walk := func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			ap := mustAbs(p)
			if ap == outDir {
				return fs.SkipDir
			}
			if !recursive && ap != in {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || kind(p) == "" {
			return nil
		}
		rel, e := filepath.Rel(in, p)
		if e != nil {
			rel = filepath.Base(p)
		}
		fi, e := d.Info()
		var sz int64
		if e == nil {
			sz = fi.Size()
		}
		items = append(items, fileItem{src: p, dst: filepath.Join(outDir, rel), label: rel, kind: kind(p), size: sz})
		return nil
	}
	if err = filepath.WalkDir(in, walk); err != nil {
		return nil, outDir, err
	}
	return items, outDir, nil
}

// ScrubWithProgress cleans a single file or a folder, reporting via prog.
// ffmpeg/ffprobe may be "" — images are still processed; videos are skipped.
func ScrubWithProgress(path string, recursive bool, ffmpeg, ffprobe string, prog Progress) (Result, error) {
	var res Result
	items, outLoc, err := collect(path, recursive)
	res.OutLoc = outLoc
	if err != nil {
		return res, err
	}
	if len(items) == 0 {
		prog.log("no supported image or video files found")
		return res, nil
	}

	var totalBytes int64
	for _, it := range items {
		totalBytes += it.size
	}
	overall := func(done, cur int64) float64 {
		if totalBytes <= 0 {
			return 0
		}
		v := float64(done+cur) / float64(totalBytes)
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}

	var completed int64
	for i, it := range items {
		idx := i + 1
		prog.status(overall(completed, 0), idx, len(items), it.label)

		var e error
		switch it.kind {
		case "image":
			e = scrubImageFile(it.src, it.dst, strings.ToLower(filepath.Ext(it.src)))
			prog.status(overall(completed, it.size), idx, len(items), it.label)
		case "video":
			if ffmpeg == "" {
				res.SkippedNoFFmpeg++
				prog.log("skipped (needs ffmpeg): " + it.label)
				completed += it.size
				prog.status(overall(completed, 0), idx, len(items), it.label)
				continue
			}
			sz := it.size
			e = stripVideoProgress(it.src, it.dst, ffmpeg, ffprobe, func(fr float64) {
				prog.status(overall(completed, int64(fr*float64(sz))), idx, len(items), it.label)
			})
		}

		if e != nil {
			res.Failed++
			prog.log("FAILED " + it.label + ": " + e.Error())
		} else {
			res.Processed++
			prog.log("cleaned " + it.label)
		}
		completed += it.size
		prog.status(overall(completed, 0), idx, len(items), it.label)
	}
	return res, nil
}

// ---- image stripping (pure Go, lossless) ----

func scrubImageFile(src, dst, ext string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var cleaned []byte
	switch ext {
	case ".jpg", ".jpeg":
		cleaned, err = stripJPEG(data)
	case ".png":
		cleaned, err = stripPNG(data)
	default:
		return fmt.Errorf("unsupported extension %q", ext)
	}
	if err != nil {
		return err
	}
	if _, _, err := image.Decode(bytes.NewReader(cleaned)); err != nil {
		return fmt.Errorf("stripped output no longer decodes: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, cleaned, 0o644)
}

func stripJPEG(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, errors.New("not a JPEG (missing SOI)")
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8)
	i := 2
	for i < len(data) {
		if data[i] != 0xFF {
			return nil, fmt.Errorf("expected 0xFF marker at offset %d", i)
		}
		for i < len(data) && data[i] == 0xFF {
			i++
		}
		if i >= len(data) {
			break
		}
		marker := data[i]
		i++
		switch {
		case marker == 0xD9:
			out = append(out, 0xFF, 0xD9)
			return out, nil
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			out = append(out, 0xFF, marker)
		default:
			if i+2 > len(data) {
				return nil, errors.New("truncated segment length")
			}
			segLen := int(data[i])<<8 | int(data[i+1])
			if segLen < 2 || i+segLen > len(data) {
				return nil, errors.New("invalid segment length")
			}
			segStart, segEnd := i, i+segLen
			drop := (marker >= 0xE0 && marker <= 0xEF) || marker == 0xFE
			if !drop {
				out = append(out, 0xFF, marker)
				out = append(out, data[segStart:segEnd]...)
			}
			i = segEnd
			if marker == 0xDA {
				start := i
				for i < len(data) {
					if data[i] == 0xFF && i+1 < len(data) {
						n := data[i+1]
						switch {
						case n == 0x00 || (n >= 0xD0 && n <= 0xD7):
							i += 2
							continue
						case n == 0xFF:
							i++
							continue
						default:
							goto doneScan
						}
					}
					i++
				}
			doneScan:
				out = append(out, data[start:i]...)
			}
		}
	}
	return out, nil
}

var pngKeep = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true,
}

func stripPNG(data []byte) ([]byte, error) {
	sig := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	if len(data) < 8 || !bytes.Equal(data[:8], sig) {
		return nil, errors.New("not a PNG (bad signature)")
	}
	out := make([]byte, 0, len(data))
	out = append(out, sig...)
	i := 8
	for i+12 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[i : i+4]))
		if length < 0 || i+12+length > len(data) {
			return nil, errors.New("invalid PNG chunk length")
		}
		ctype := string(data[i+4 : i+8])
		chunkEnd := i + 12 + length
		if pngKeep[ctype] {
			out = append(out, data[i:chunkEnd]...)
		}
		i = chunkEnd
		if ctype == "IEND" {
			break
		}
	}
	return out, nil
}

func withSuffix(path, suffix string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + suffix + ext
}

func mustAbs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
