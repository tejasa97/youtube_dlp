# YouTube search extractor evidence

Implemented scope is public `ytsearch:<query>` (one entry), `ytsearchN:<query>`
(N 1–50), `ytsearchall:<query>` (locally capped at 50), and exact public
`youtube.com` / `www.youtube.com` `/results` or `/search` URLs containing
`search_query` or `q`. Pseudo URLs request the upstream video-only parameter
`EgIQAfABAQ==`. Upstream `ytsearchall` is unbounded; the local cap is an
intentional resource-safety deviation.

`internal/extractor/youtube_search_test.go` uses deterministic synthetic
initial and continuation data, with provenance in
`conformance/extractors/youtube_search/PROVENANCE.md`. It covers initial and
continuation parsing, ordered videos/Shorts, count cap, lazy/reusable
iteration, continuation request fields, hostile routing, malformed input,
authentication/rate/network categorization, cancellation, and repeated-token
loop prevention. Parser and target fuzz tests cover malformed input.

Not claimed: YouTube Music, channel/playlist/hashtag results, arbitrary
filters or sorts, authenticated search, or live-site stability.
