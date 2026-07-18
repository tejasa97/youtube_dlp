# Internet Archive extractor fixture provenance

`success.json` is a deterministic, entirely synthetic response shaped like the
public `https://archive.org/metadata/{identifier}` API. It contains no captured
response, media, credential, cookie, private URL, or executable.

The expected behavior was derived from `ArchiveOrgIE` in the read-only pinned
yt-dlp reference checkout `/Users/tejas/projects/yt-dlp-reference` at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/extractor/archiveorg.py` lines 32–378 as present at that commit. Derived
semantics include `details`/`embed` routing, item/file IDs, association of
derivatives through `original`, public-file filtering, source preference,
metadata inheritance, subtitles, thumbnails, and multi-entry playlist output.
No upstream source or fixture bytes were copied.

The synthetic inventory intentionally arrives out of lexical order so tests
prove deterministic entry and format ordering. It also includes a private file
and a non-media metadata file to prove they are not exposed.
