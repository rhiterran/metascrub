# metascrub

One Windows app that removes metadata / personal information from **both images and videos**, by writing clean **copies**. Point it at a single file or a whole folder. Originals are never modified, nothing is re-encoded, and there is nothing else to install.

## What it handles

- **Images**: `.jpg`, `.jpeg`, `.png`
- **Videos**: `.mp4`, `.m4v`, `.mov`, `.3gp`, `.3g2`, `.f4v`, `.mkv`, `.webm`, `.avi`, `.wmv`, `.asf`, `.flv`, `.ogv`, `.ts`, `.m2ts`, `.mts`, `.mpg`, `.mpeg`, `.vob`

Everything is processed by the app itself. Files are recognised by their content, not just their extension, so a mislabelled file is still handled correctly. Any other file in a folder (documents, archives, etc.) is ignored.

## Using the app

1. Run `metascrub-gui.exe`. It is not code-signed yet, so if SmartScreen warns, click **More info → Run anyway**.
2. **Browse…** to pick a folder, or paste a single file's path into the box.
3. Leave **Include subfolders** ticked to process the whole tree.
4. Click **Remove metadata → copies**. A progress bar and a status line ("File 2 of 5 — 37% …") update live, and each finished file is listed in the log with what was removed from it. A summary appears when done.

The work runs on a background thread, so the window stays responsive even for very large files or a folder of 100+ GB. The progress bar is weighted by total bytes.

Output:
- A **folder** → `<folder>-cleaned`, mirroring subfolders.
- A **single file** → `<name>-nometa.<ext>` beside the original.

## What it removes

- **JPEG**: all `APPn`/`COM` segments: EXIF, GPS, IPTC, XMP, JFIF, thumbnails, comments.
- **PNG**: all non-essential chunks: text, timestamps, DPI, color profiles (keeps only what reproduces the pixels).
- **MP4 / MOV / M4V / 3GP / F4V**: title, artist, comments, GPS location, Apple/Android/Samsung metadata keys, cover art, XMP, creation and modification dates, encoder and device names, chapter names, and the contents of telemetry tracks: GoPro / DJI / Google GPS tracks, Sony real-time metadata, time-of-day timecode, and dashcam GPS logs. Fragmented MP4 files are covered too.
- **MKV / WebM**: tags, chapters, title, recording date, file names, muxing/writing application names, track names, and attached cover images. Font attachments used by subtitles are kept.
- **AVI**: INFO tags (title, software, comments), recording date, stream names, XMP and camera-maker chunks.
- **WMV / ASF**: title, author, copyright, description, WM/ tags (encoding software and time), branding, markers, script commands, creation date.
- **FLV**: encoder, creator, title and other text tags, creation dates, XMP.
- **OGV / Ogg**: comment tags (title, artist, encoder, date, cover art) of Theora, Vorbis, Opus, FLAC and Speex streams.
- **TS / M2TS / MTS**: broadcast service information: channel and provider names, programme guide entries, broadcast date and time.
- **MPG / MPEG / VOB**: these containers have no metadata fields, so they are copied unchanged.

## How videos are cleaned (lossless by design)

The app reads only the file's structure, skipping over the audio and video data, and finds where the metadata sits. Each piece of metadata is then overwritten in place with something of exactly the same size: a metadata box becomes empty padding, a date becomes zero, a text value becomes blank. The copy is written in a single pass with those changes applied.

Because nothing changes size, every index and offset in the file stays valid, and the audio and video data in the copy is **byte-for-byte identical** to the original. Nothing is re-muxed or re-encoded. It is also fast: a 0.9 GB MP4 is cleaned in about 4 seconds, roughly as fast as copying the file.

What remains is structural and carries no personal information: format identifiers (e.g. MP4 brand IDs), language codes, and empty padding where the metadata used to be.

Limitation: a few cameras also embed data inside the compressed video stream itself (for example some AVCHD camcorders write date and GPS there). Removing that would require changing the video data, so it is left as is.

## Build from source

Requires Go 1.24+ (standard library only).

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H=windowsgui -s -w" -o metascrub-gui.exe .
```

Run the tests with `go test ./...`. The end-to-end test uses ffmpeg only to create its sample input files, and is skipped if ffmpeg isn't installed.

## Source layout

- `scrub.go`: core: file discovery, routing, byte-weighted progress, and the JPEG/PNG strippers.
- `video.go`: video format detection and the shared single-pass copy that applies the changes.
- `vid_mp4.go`, `vid_mkv.go`, `vid_avi.go`, `vid_asf.go`, `vid_flv.go`, `vid_ogg.go`, `vid_ts.go`: one parser per container family.
- `ffmpeg_fallback.go`: optional compatibility path, used only if a video's structure can't be read (e.g. a damaged file) and ffmpeg happens to be installed.
- `main_windows.go`: native Win32 GUI (`//go:build windows`).
- `main_other.go`: CLI for Linux/macOS (`//go:build !windows`), used for testing.
- `procattr_windows.go` / `procattr_other.go`: hide the console window when the fallback runs on Windows.
- `video_test.go`: end-to-end test across all video formats.

## Verified behavior

Sample files in every supported video format were created with planted metadata (title, artist, comment, GPS location, creation date, chapters, track and handler names, cover art, timecode, broadcast service names) and cleaned. For every file: the output is the same size as the original, none of the planted values remain anywhere in the output bytes, the audio and video packets are identical to the original, and the output decodes without errors. Edge cases tested: fragmented MP4, MOV with timecode and Apple location keys, MKV with CRC-32 checksums, live-recorded MKV with unknown element sizes, Blu-ray M2TS, Opus in Ogg, truncated files (cleaned when usable, reported clearly when not), and unrecognised files (reported, not touched). Images come out pixel-identical. The GUI binary compiles to a valid PE32+ Windows executable; the window itself was not click-tested in the build environment.

## Releases & signing

CI (`.github/workflows/build.yml`) runs the tests and cross-compiles the Windows x64 binary on every push and tag. Code signing through the [SignPath Foundation](https://signpath.org/) open-source program is planned; until it is connected, released binaries are unsigned.

## License

MIT — see [LICENSE](LICENSE).
