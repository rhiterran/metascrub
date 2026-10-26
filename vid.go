package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// FindFFmpeg returns the ffmpeg path: a copy next to this executable if present,
// otherwise whatever is on PATH, otherwise "".
func FindFFmpeg() string { return findTool("ffmpeg") }

// FindFFprobe locates ffprobe, preferring the same folder as the given ffmpeg
// (winget's ffmpeg package ships both together), then PATH.
func FindFFprobe(ffmpeg string) string {
	name := exeName("ffprobe")
	if ffmpeg != "" {
		cand := filepath.Join(filepath.Dir(ffmpeg), name)
		if st, e := os.Stat(cand); e == nil && !st.IsDir() {
			return cand
		}
	}
	return findTool("ffprobe")
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func findTool(base string) string {
	name := exeName(base)
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if st, e := os.Stat(cand); e == nil && !st.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath(base); err == nil {
		return p
	}
	return ""
}

// probeDurationUS returns the media duration in microseconds, or 0 if unknown.
func probeDurationUS(ffprobe, src string) int64 {
	if ffprobe == "" {
		return 0
	}
	cmd := exec.Command(ffprobe, "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nokey=1:noprint_wrappers=1", src)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || sec <= 0 {
		return 0
	}
	return int64(sec * 1e6)
}

// stripVideoProgress remuxes one file with all metadata removed (lossless
// stream copy) and reports fractional progress via onFrac (0..1) when the
// duration is known.
func stripVideoProgress(src, dst, ffmpeg, ffprobe string, onFrac func(float64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	durUS := probeDurationUS(ffprobe, src)

	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-progress", "pipe:1", "-y",
		"-i", src,
		"-map", "0",
		"-map_metadata", "-1",
		"-map_metadata:s", "-1",
		"-map_chapters", "-1",
		"-c", "copy",
		"-bitexact",
	}
	switch strings.ToLower(filepath.Ext(dst)) {
	case ".mp4", ".m4v", ".mov":
		args = append(args, "-movflags", "+faststart", "-empty_hdlr_name", "1")
	}
	args = append(args, dst)

	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key, val := line[:eq], line[eq+1:]
		if (key == "out_time_us" || key == "out_time_ms") && durUS > 0 && onFrac != nil {
			if us, e := strconv.ParseInt(strings.TrimSpace(val), 10, 64); e == nil {
				f := float64(us) / float64(durUS)
				if f > 1 {
					f = 1
				}
				onFrac(f)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v: %s", err, tail(errBuf.String()))
	}
	if onFrac != nil {
		onFrac(1)
	}
	return nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(no ffmpeg output)"
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ")
}
