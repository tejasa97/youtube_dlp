# YouTube Music browse evidence

Behavior is scoped to the public Music `/browse/` handling in yt-dlp reference
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/youtube/_tab.py:YoutubeTabIE` (Music album `MP*` resolution
via `web_music` / WEB_REMIX, VL playlist and UC artist `/browse/` routing, and
`_music_reponsive_list_entry` browse emission). The pinned checkout is read-only
behavioral context only.

Supported routes are exact HTTP(S) `music.youtube.com/browse/{id}` URLs for the
registered families:

- albums: `MPRE...` must resolve to an exact `music.youtube.com/playlist?list=`
  canonical playlist identity from webpage microformat or WEB_REMIX
  resolve+browse; otherwise extraction fails closed (`ErrUnavailable` /
  `ErrInvalidMetadata` as appropriate) and never returns a bare `MPRE` id
- artists: `UC...` and `MPLA`+UCID
- playlists: `VL`+playlistId
- podcasts: `MPSP...`

Anonymous public pages succeed through cookie-isolated WEB_REMIX GET/page and
JSON requests. Cookie isolation is required before any Music browse network
call; missing isolation fails closed with no request. Initial HTML is fetched
with `DoWithoutCookies` (bounded body, categorized status handling) and never
falls back to jar-backed `ReadPage`. Continuations, resolve, and browse posts
use the same isolation boundary, stay on `music.youtube.com`, advertise
WEB_REMIX only, bound to 100 entries, and rotate visitor data. Resolve browse
endpoint identity must match the requested album id; oversized/malformed params
and hostile canonical playlist URLs fail closed. Logged-in pages, WEB client
identity on a Music page, and premium/sign-in alerts fail closed. Authenticated
or premium Music success is not claimed.

Music search restores browse URL emission only for these registered families
so default playlist expansion selects `youtube_music_browse` instead of the
generic YouTube extractor.
