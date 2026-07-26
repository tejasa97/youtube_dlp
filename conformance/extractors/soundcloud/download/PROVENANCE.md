# SoundCloud original-download provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

Reference: `yt_dlp/extractor/soundcloud.py`, `SoundcloudIE._extract_info_dict`,
and `yt_dlp/utils/_utils.py`, `urlhandle_detect_ext`.

Derived behavior:

- tracks marked both `downloadable` and `has_downloads_left` request
  `tracks/{id}/download` with the current client ID and optional secret token;
- 401 and 403 make the optional original unavailable, while 429 propagates;
- a successful `redirectUri` is probed with HEAD and emitted first as
  `format_id=download`, quality 10, note `Original`, and `vcodec=none`;
- extension precedence is Content-Disposition filename, Amazon object-name
  metadata, Amazon file-type metadata, then Content-Type.

All request/response fixtures in Go tests are synthetic and secret-free. The Go
port additionally caps redirects at 10, requires credential-isolated
no-redirect requests for every HEAD hop, validates every HTTPS destination,
rejects IP literals and ambiguous paths, and bounds all metadata.

Explicit SoundCloud login and premium subscription formats remain out of scope.
