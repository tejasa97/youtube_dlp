# Public/API extractor corpus

These minimal, synthetic JSON and HTML fixtures are schema-derived from the
pinned yt-dlp checkout at `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:
`dailymotion.py`, `reddit.py`, `twitter.py`, `bandcamp.py`, `mixcloud.py`,
`rumble.py`, and `bilibili.py`.

They are intentionally not captures. Hosts are the reserved `.invalid` domain;
there are no account cookies, bearer tokens, signed URLs, personal data, or
production media bytes. The fixtures exercise public response shapes only.

Explicit deviations: Dailymotion uses anonymous player metadata rather than
the reference OAuth/GraphQL path; Twitter uses its public syndication endpoint
rather than an embedded bearer-token GraphQL flow; Bilibili uses page hydration
rather than signed WBI calls; Bandcamp does not follow customer download links.
Bilibili DASH codec-side normalization follows `BiliBiliBaseIE.extract_formats`:
audio tracks expose `acodec` with `vcodec=none`, while video tracks expose
`vcodec` with `acodec=none`. The synthetic corpus intentionally also covers an
omitted codec name, preserving the known media kind with an `unknown` marker.
