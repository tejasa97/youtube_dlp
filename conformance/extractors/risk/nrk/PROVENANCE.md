# NRK risk fixtures

Synthetic playback, catalog, Skole, and HTML playlist fixtures authored on
2026-07-30 from `yt_dlp/extractor/nrk.py` at pinned upstream commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. Example media hosts and invented
programme IDs avoid any production or user-derived data.

## Supported public family

- `nrk` opaque playback for programme/channel IDs and podcast UUIDs
- `nrktv` programme-ID shims on tv/radio/nrksuper paths
- `nrktv_direkte` live channel aliases
- `nrktv_episode` canonical `/serie/.../sesong/N/episode/M` resolution
- `nrktv_episodes` legacy `/program/episodes/...` HTML listings
- `nrktv_season` and `nrktv_series` catalog playlists with lazy pagination
- `nrk_radio_podkast` podcast episode UUID shims
- `nrk_skole` educational `mediaId` lookup via the Skole API
- `nrk_playlist` nrk.no article pages with embedded rich video widgets

Catalog and HTML playlist entries reenter `nrk:{id}` transparently. Skole and
episode resolution use credential-isolated no-redirect HTTP where required.

## Known deviations

- Legacy `nrk.no/video/*`, `v8.psapi.nrk.no/mediaelement/`, encrypted assets,
  authenticated-only flows, CDN hostname fallback rewriting, and multi-part
  `#del=N` fragments remain out of scope.
- Series catalog season delegation uses linked-season metadata only; it does not
  recursively expand every embedded season subtree in one playlist result.
- Geo/auth/unavailable states fail closed with typed extractor errors.
