# BBC iPlayer risk fixtures

Synthetic webpage and media-selector fixtures authored on 2026-07-18 from
`yt_dlp/extractor/bbc.py` at pinned upstream commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. URLs use reserved example domains;
the content is invented and has no account or location data.

Known deviation: the Go scope supports programme/episode pages plus iPlayer
episode/group playlists. It does not implement legacy EMP XML, ASX, RTMP, HDS,
or the general BBC article embed matrix. Media-selector HLS, DASH, direct HTTP,
captions, UK geo errors, and sign-in failures are preserved.
