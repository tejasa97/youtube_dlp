# YouTube Music search evidence

Behavior is scoped to the public URL extractor in yt-dlp reference commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/youtube/_search.py:YoutubeMusicSearchURLIE`.

Supported routes are exact HTTP(S) `music.youtube.com/search` URLs with `q` or
`search_query`. `#songs` and `#videos` use the reference's pinned `sp` values.
The implementation uses WEB_REMIX continuation requests and returns only
recognized video IDs as normal YouTube watch entries. It is bounded to 50
entries and shared continuation machinery prevents cursor loops.

Excluded: albums, artists, playlists, podcasts, arbitrary `sp`, authenticated
or premium success, full Music metadata, and live-specific compatibility.
