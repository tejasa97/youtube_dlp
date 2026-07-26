# SoundCloud player/embed provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/soundcloud.py` |
| Reference class | `SoundcloudEmbedIE` |
| Reference function(s) | `_VALID_URL`, `_EMBED_REGEX`, `_real_extract` |
| Go entry | `internal/extractor.NewSoundCloudEmbed` |

## Derived behavior

- `w.soundcloud.com`, `player.soundcloud.com`, and `p.soundcloud.com` player
  URLs unwrap their encoded `url` query into a SoundCloud URL result. Exact
  apex `soundcloud.com/player` is also accepted for the pinned iframe pattern.
- A player-level `secret_token` replaces the target token.
- Declared HTML and JSON-LD player embeds are routed through the same canonical
  parser without fetching the iframe.

## Fixture construction

Tests use synthetic, license-safe URLs only. No remote page or media bytes are
committed because player unwrapping is a pure URL operation.

## Go hardening / deliberate deviations

- The unwrapped target must be accepted by the native SoundCloud classifier;
  arbitrary redirect targets fail closed.
- Hosts and `/player` paths are exact. Userinfo, ports, fragments, encoded
  separators, malformed or duplicate parameters, invalid tokens, and oversized
  URLs fail closed.
- Unrelated player and target query parameters are discarded. Only a validated
  `s-*` secret token survives canonicalization.
