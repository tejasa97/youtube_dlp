# Playlist-concat post-processing boundary

`--concat-playlist` is a closed policy surface: `never`, `always`, or
`multi_video` (the CLI default). `never` is a no-op. `always` concatenates a
materialized playlist with at least two successful media entries.
`multi_video` is a safe no-op unless the typed playlist result carries an
explicit `multi_video` marker. The current registered ordinary-playlist
extractors do not emit that marker, so callers must choose `always` for those
playlists. This prevents the CLI default from silently concatenating ordinary
playlists.

Entries are concatenated in selected playlist order, with a maximum of 128
inputs. Each input must be a regular non-symlink file. FFprobe compatibility
checks require the same stream count and ordered codec type/name and video
dimensions. The destination is rendered through the dedicated `pl_video:`
template/path classes, remains confined beneath the output root, and uses the
post-overwrite policy. The concat list is private, bounded, and passed only to
the typed ffmpeg concat operation; no shell or arbitrary ffmpeg arguments are
accepted.

Concatenation publishes through ffmpeg's private atomic output. A compatibility
or ffmpeg failure leaves every already-committed child and playlist sidecar
artifact available, preserves an existing playlist destination, removes concat
temporary files, and does not create a separate archive record. The child
archive records retain their existing per-entry semantics. Simulation and
skip-download do not invoke the concat stage.
