# metascrub

One Windows app that removes metadata / personal information from **both images and videos**, by writing clean **copies**. Point it at a single file or a whole folder. Originals are never modified, and nothing is re-encoded — it's lossless.

## What it handles

- **Images** — `.jpg`, `.jpeg`, `.png`. Stripped instantly in-process, no dependencies.
- **Videos** — `.mp4`, `.m4v`, `.mov`, `.mkv`, `.webm`, `.avi`, `.wmv`, `.flv`, `.mpg/.mpeg`, `.m2ts/.mts/.ts`, `.3gp/.3g2`, `.ogv`, `.vob`, `.asf`, `.f4v`. Handled via `ffmpeg`.

Any other file in a folder (documents, archives, etc.) is ignored.

## ffmpeg (only needed for videos)

Images work with no setup. To process videos, `ffmpeg` must be available — it's free and open-source. Install it once:

```powershell
winget install Gyan.FFmpeg
```

Then reopen the app, or drop an `ffmpeg.exe` next to `metascrub-gui.exe`. If ffmpeg isn't found, images are still cleaned and videos are skipped with a note (the app won't fail the whole run).

## Using the app

1. Run `metascrub-gui.exe`. If SmartScreen warns (it's unsigned), click **More info → Run anyway**.
2. **Browse…** to pick a folder, or paste a single file's path into the box.
3. Leave **Include subfolders** ticked to process the whole tree.
4. Click **Remove metadata → copies**. A progress bar and a status line ("File 2 of 5 — 37% …") update live, and each finished file is listed in the log. A summary appears when done.

The work runs on a background thread, so the window stays responsive and can be watched (or minimized) even for very large files or a folder of 100+ GB. The progress bar is weighted by total bytes, and for videos it advances using ffmpeg's own progress output (per-file percent needs `ffprobe`, which ships alongside `ffmpeg`).

Output:
- A **folder** → `<folder>-cleaned`, mirroring subfolders.
- A **single file** → `<name>-nometa.<ext>` beside the original.

## What it removes

- **JPEG** — all `APPn`/`COM` segments: EXIF, GPS, IPTC, XMP, JFIF, thumbnails, comments.
- **PNG** — all non-essential chunks: text, timestamps, DPI, color profiles (keeps only what reproduces the pixels).
- **Video** — global and per-stream tags across every stream: title, artist, comment, GPS location, creation date, encoder, handler names; chapters are dropped too.

Lossless: image pixel data and video/audio streams come out byte-identical to the originals; only metadata changes. Residual items (MP4 brand IDs, `language=und`, a zeroed vendor ID, a generic `Lavf` tag on MKV) are structural and carry no personal information.

## Build from source

Requires Go 1.24+ (standard library only). ffmpeg is a runtime dependency for video, not a build one.

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H=windowsgui -s -w" -o metascrub-gui.exe .
```

## Source layout

- `scrub.go` — core: file discovery, routing, byte-weighted progress, and the pure-Go JPEG/PNG strippers.
- `vid.go` — ffmpeg/ffprobe lookup, duration probe, and the lossless remux with `-progress` streaming.
- `main_windows.go` — native Win32 GUI (`//go:build windows`).
- `main_other.go` — CLI for Linux/macOS (`//go:build !windows`), used for testing.
- `procattr_windows.go` / `procattr_other.go` — suppress the console window for ffmpeg on Windows.

## Verified behavior

The full pipeline was run on a mixed folder (JPEG with EXIF+GPS, PNG with text/DPI, MP4 with title/artist/date/comment/GPS + per-stream handler names, MKV) plus a non-media file. Results: every image came out pixel-identical with metadata removed; every video came out stream-identical with personal tags removed; the non-media file was ignored; single-file mode and the ffmpeg-missing skip path both behaved correctly; and ffmpeg's `-progress` stream was confirmed to yield smooth 0→100% updates for long-running operations. The GUI binary compiles clean to a valid PE32+ Windows executable; the window itself was not click-tested in the build environment.

## Releases & signing

CI (`.github/workflows/build.yml`) cross-compiles the Windows x64 binary on every push and tag. Released binaries are code-signed for free via the [SignPath Foundation](https://signpath.org/) open-source program, which signs the artifact produced by this pipeline.

## License

MIT — see [LICENSE](LICENSE).
