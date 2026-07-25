# YouTube Music search evidence

Behavior is scoped to the public URL extractor in yt-dlp reference commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/youtube/_search.py:YoutubeMusicSearchURLIE`.

Supported routes are exact HTTP(S) `music.youtube.com/search` URLs with `q` or
`search_query`. `#songs` and `#videos` use the reference's pinned `sp` values.
The implementation uses cookie-isolated WEB_REMIX continuation requests and
returns recognized video IDs as normal YouTube watch entries plus playlist,
channel, and registered Music browse URL results. It is bounded to 50 entries
and shared continuation machinery prevents cursor loops.

Excluded: arbitrary `sp`, authenticated or premium Music success, full Music
metadata beyond typed URL results, and live-specific compatibility.
