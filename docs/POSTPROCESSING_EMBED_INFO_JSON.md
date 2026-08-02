# Bounded embedded info JSON

This branch adds `--embed-info-json` and `--no-embed-info-json` as a typed,
transactional post-processing stage.

The pinned reference is `/Users/tejas/projects/yt-dlp-reference` at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, read only. Its
`yt_dlp/postprocessor/ffmpeg.py` `FFmpegMetadataPP` stage attaches `info.json`
only to `mkv`/`mka`, replaces an existing JSON attachment, and labels the
attachment with `mimetype=application/json` and `filename=info.json`.

The Go contract is deliberately bounded:

- only `mkv` and `mka` are accepted;
- the payload is cleaned with the existing related-info sanitizer, excluding
  path/private lifecycle fields, and must be valid JSON no larger than 256 KiB;
- only streams whose ffprobe `codec_type` is `attachment` and whose filename or
  MIME tag identifies `info.json` are replaced; ordinary media streams are
  never removed because of a tag alone;
- ffmpeg receives a private payload file and a typed argv plan, never a shell
  command or arbitrary postprocessor arguments;
- ffmpeg output is atomic, and the active output transaction snapshots the
  published media before in-place metadata stages so later failure restores it;
- simulation and skip-download paths do not create an attachment.

The product coverage generates a license-free MKV, downloads it through the
registered generic extractor, extracts the attachment bytes with ffmpeg, and
compares them byte-for-byte with the cleaned Info JSON. It also reruns the
stage to verify replacement, cancels after the post-process stage is entered,
and forces a later thumbnail failure to verify destination restoration,
temporary cleanup, and archive non-publication.

This branch does not include chapter splitting, playlist concatenation, xattrs,
or policy-driven fixups. Those are separate ordered stack boundaries.
