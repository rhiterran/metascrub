package main

// End-to-end test: builds sample files carrying planted metadata in every
// supported video format (using ffmpeg, only to create the inputs), cleans
// them with the native code, then checks that (1) no planted value survives
// anywhere in the output bytes, (2) the audio/video packets are identical to
// the original, and (3) the output decodes without errors.
// Skipped when ffmpeg is not installed.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeVideoScrub(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed (needed only to create test inputs)")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	ff := func(args ...string) {
		t.Helper()
		cmd := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
	meta := ";FFMETADATA1\ntitle=SECRETTITLE\nartist=SECRETARTIST\ncomment=SECRETCOMMENT\n" +
		"location=+54.6872+025.2797/\ncreation_time=2026-05-01T10:20:30Z\n" +
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=500\ntitle=SECRETCHAPTER\n"
	if err := os.WriteFile(filepath.Join(dir, "meta.txt"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	src := []string{"-f", "lavfi", "-i", "testsrc=d=1:s=160x120:r=25", "-f", "lavfi", "-i", "sine=d=1", "-i", "meta.txt",
		"-map", "0", "-map", "1", "-map_metadata", "2", "-shortest"}
	chap := []string{"-map_chapters", "2"}
	trk := []string{"-metadata:s:v", "title=SECRETTRACK", "-metadata:s:v", "handler_name=SECRETHANDLER"}
	cases := map[string][]string{
		"a.mp4":  append(append(append([]string{}, chap...), trk...), "-c:v", "mpeg4", "-c:a", "aac", "-movflags", "+use_metadata_tags", "-timecode", "10:20:30:00"),
		"a.mov":  append(append(append([]string{}, chap...), trk...), "-c:v", "mpeg4", "-c:a", "aac", "-movflags", "+use_metadata_tags+frag_keyframe"),
		"a.mkv":  append(append(append([]string{}, chap...), trk...), "-c:v", "mpeg4", "-c:a", "aac"),
		"a.avi":  append(append([]string{}, trk...), "-c:v", "mpeg4", "-c:a", "mp3"),
		"a.wmv":  {"-c:v", "wmv2", "-c:a", "wmav2"},
		"a.flv":  {"-c:v", "flv1", "-c:a", "mp3", "-ar", "44100"},
		"a.ogv":  {"-c:v", "libtheora", "-c:a", "libvorbis"},
		"a.ts":   {"-c:v", "mpeg2video", "-c:a", "mp2", "-metadata", "service_name=SECRETSERVICE"},
		"a.webm": append(append([]string{}, chap...), "-c:v", "libvpx", "-c:a", "libopus"),
	}
	for name, extra := range cases {
		args := append(append(append([]string{}, src...), extra...), filepath.Join("in", name))
		ff(args...)
	}

	res, err := ScrubWithProgress(in, true, "", "", Progress{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Processed != len(cases) {
		t.Fatalf("processed=%d failed=%d, want %d/0", res.Processed, res.Failed, len(cases))
	}

	hash := func(p string) []byte {
		out, err := exec.Command("ffmpeg", "-v", "error", "-i", p, "-map", "0:V?", "-map", "0:a?",
			"-c", "copy", "-f", "streamhash", "-").Output()
		if err != nil {
			t.Fatalf("streamhash %s: %v", p, err)
		}
		return out
	}
	for name := range cases {
		orig := filepath.Join(in, name)
		clean := filepath.Join(dir, "in-cleaned", name)
		b, err := os.ReadFile(clean)
		if err != nil {
			t.Fatal(err)
		}
		ob, _ := os.ReadFile(orig)
		if len(b) != len(ob) {
			t.Errorf("%s: size changed %d -> %d", name, len(ob), len(b))
		}
		for _, s := range []string{"SECRET", "54.6872", "2026-05-01"} {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%s: %q still present", name, s)
			}
		}
		if !bytes.Equal(hash(orig), hash(clean)) {
			t.Errorf("%s: audio/video packets differ from the original", name)
		}
		out, _ := exec.Command("ffmpeg", "-v", "error", "-i", clean, "-map", "0", "-f", "null", "-").CombinedOutput()
		if s := strings.TrimSpace(string(out)); s != "" {
			t.Errorf("%s: decode errors: %s", name, s)
		}
	}
}
