# Bandcamp collection fixtures

Synthetic fixtures modeling the pinned yt-dlp checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically `bandcamp.py`
`BandcampUserIE._yield_items` (lines 529-536) and `BandcampWeeklyIE._real_extract`
(lines 441-475). They are not captures of production pages or API responses.

## Derivation

- `user_type1.html` exercises the `li data-item-id` anchor discovery shape,
  including merch exclusion, duplicate suppression, and hostile-host rejection.
- `user_type2.html` exercises the `trackTitle` fallback shape used when the
  primary list markup is absent.
- `user_type3.html` exercises the `music-grid` `data-client-items` JSON
  `page_url` discovery shape.
- `user_combined.html` preserves pinned occurrence order across the primary list
  and music-grid shapes, including duplicate suppression.
- `weekly_player_response.json` mirrors the `player_data_web` radio response
  fields consumed by `BandcampWeeklyIE`.

## Duplicate policy

Playlist entry order follows pinned `playlist_from_matches` / `orderedSet`
behavior: discovery preserves first occurrence order and suppresses later
duplicates of the same canonical child URL.

## Sanitization

Hosts use the reserved `.invalid` domain or the synthetic `fixture.bandcamp.com`
subdomain. Stream URLs, tokens, and image identifiers are synthetic. No account
cookies, bearer tokens, production media bytes, or personal data are included.

## Explicit deviations

Track and album child extraction still uses the existing `bandcamp` extractor on
public `data-tralbum` pages. Customer purchase/download hand-offs remain
unsupported.
