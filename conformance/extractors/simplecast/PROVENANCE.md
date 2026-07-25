# simplecast provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/simplecast.py` |
| Reference class | `SimplecastIE` |
| Reference function(s) | `_real_extract / SimplecastBaseIE` |
| Go entry | `internal/extractor` constructors `New*` for key `simplecast` |

## Derived facts (copied from reference behavior)
- api.simplecast.com/episodes/{uuid}
- player.simplecast.com/{uuid}
- audio_file.url / audio_file_url / enclosure_url selection followed by the
  pinned `clean_podcast_url` prefix cleanup rules

## Fixture construction
- Synthetic, license-safe, secret-free: `episode.json` with a nested
  Podsights/Gumball/Chartable/Chartable-Radio/Podsucker prefix chain
- No copyrighted media bytes committed.

## Go hardening / deliberate deviations
- UUID path validation
- HTTPS media policy
