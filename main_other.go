//go:build !windows

// CLI entry point for non-Windows, used to test the full pipeline on Linux/macOS.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	r := flag.Bool("r", true, "recurse into subfolders")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: metascrub <file-or-folder>")
		os.Exit(2)
	}
	ff := FindFFmpeg()
	fp := FindFFprobe(ff)

	lastPct := -1
	res, err := ScrubWithProgress(flag.Arg(0), *r, ff, fp, Progress{
		Log: func(s string) { fmt.Println(s) },
		Status: func(frac float64, idx, total int, name string) {
			if p := int(frac * 100); p != lastPct {
				lastPct = p
				fmt.Fprintf(os.Stderr, "\r[%3d%%] file %d/%d  %-40.40s", p, idx, total, name)
			}
		},
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "metascrub:", err)
		os.Exit(1)
	}
	fmt.Printf("cleaned=%d failed=%d\noutput: %s\n", res.Processed, res.Failed, res.OutLoc)
	if res.Failed > 0 {
		os.Exit(1)
	}
}
