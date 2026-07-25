# YouTube Music browse evidence

Behavior is scoped to the public Music `/browse/` handling in yt-dlp reference
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/youtube/_tab.py:YoutubeTabIE` (Music album `MP*` resolution
via `web_music` / WEB_REMIX, VL playlist and UC artist `/browse/` routing, and
`_music_reponsive_list_entry` browse emission).

Supported routes are exact HTTP(S) `music.youtube.com/browse/{id}` URLs for the
registered families:

- albums: `MPRE...` (resolve to playlist identity when microformat/API provide
  `urlCanonical`, otherwise fail closed)
- artists: `UC...` and `MPLA`+UCID
- playlists: `VL`+playlistId
- podcasts: `MPSP...`

Anonymous public pages succeed through WEB_REMIX page and continuation
requests. Continuations are cookie-isolated, bounded to 100 entries, rotate
visitor data, and never use the WEB client identity. Logged-in pages, WEB
client identity on a Music page, premium/sign-in alerts, and missing cookie
isolation fail closed. Authenticated or premium Music success is not claimed.

Music search restores browse URL emission only for these registered families
so default playlist expansion selects `youtube_music_browse` instead of the
generic YouTube extractor.
