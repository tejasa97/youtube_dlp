# panopto_playlist provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/panopto.py` |
| Reference class | `PanoptoPlaylistIE` |
| Reference function(s) | `_real_extract` / `_entries` |
| Go entry | `NewPanoptoPlaylist` |

## Derived facts
- `pid=` playlist id → Api/Playlists/{pid} → SessionListId → Api/SessionLists items

## Fixture construction
- Synthetic playlist.json + sessionlist.json

## Go hardening
- LazyFirstPageEntries
- ViewerUri host must be panopto family; entry URL rebuilt from validated host+id
- TypeName Session only; UUID dedupe; panoptoMaxEntries
