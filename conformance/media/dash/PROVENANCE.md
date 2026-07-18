# DASH conformance fixture provenance

The fixture in this directory is an independently authored, license-safe MPD
used to exercise inherited `SegmentTemplate` attributes, a timeline with
`r="-1"`, and a deterministic dynamic presentation boundary.

Behavioral expectations were reviewed against the pinned yt-dlp reference at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally
`yt_dlp/extractor/common.py` (`_parse_mpd_periods`, including multisegment
inheritance and `$Time$` expansion). The fixture contains no copied media or
site response. Its synthetic timestamps and URLs were created for this Go
test corpus.

The Go implementation intentionally improves one edge relative to the pinned
reference parser: ISO/IEC 23009-1 negative-repeat timelines are expanded to the
next explicit `S@t`, or to a known period/publish boundary. A final unbounded
negative repeat remains a categorized unsupported-timeline error rather than
guessing an infinite sequence.
