# DASH conformance fixture provenance

The fixtures in this directory are independently authored, license-safe MPD and
binary structures used to exercise DASH segment addressing, timeline expansion,
and SIDX-based SegmentBase indexRange support.

Behavioral expectations were reviewed against the pinned yt-dlp reference at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally:

- `yt_dlp/extractor/common.py` (`_parse_mpd_periods`, including multisegment
  inheritance and `$Time$` expansion)
- `yt_dlp/extractor/common.py` (`_parse_mpd_segments`, SegmentBase/indexRange
  handling and SIDX expansion logic)
- ISO/IEC 23009-1:2019 §5.3.9.4 (Segment index box)
- ISO/IEC 14496-12:2022 §8.16.3 (SegmentIndexBox 'sidx')

## Fixture inventory

| File | Purpose |
|------|---------|
| `negative_repeat.mpd` | Inherited SegmentTemplate, timeline `r="-1"`, dynamic boundary |
| `negative_repeat.expected.json` | Expected parse output for the above |
| `sidx_indexrange.mpd` | SegmentBase with indexRange at representation and adaptation-set levels |
| `sidx_indexrange.expected.json` | Expected parse output showing marker segments |
| `sidx_v0_two_refs.hex` | Synthetic SIDX v0 binary box with 2 references |

## Synthetic data attestation

- All payload bytes and URLs are synthetic; no copied media, credentials, or
  signed production URLs are present.
- The SIDX binary fixture (`sidx_v0_two_refs.hex`) was constructed from the
  ISO-BMFF SegmentIndexBox layout (ISO/IEC 14496-12 §8.16.3) with synthetic
  reference sizes and durations.
- The MPD fixtures use `example.test` domain URLs that cannot resolve.

## Implementation notes

The Go implementation intentionally improves one edge relative to the pinned
reference parser: ISO/IEC 23009-1 negative-repeat timelines are expanded to the
next explicit `S@t`, or to a known period/publish boundary. A final unbounded
negative repeat remains a categorized unsupported-timeline error rather than
guessing an infinite sequence.

Dynamic manifests with SegmentBase/SIDX are explicitly rejected
(`ErrUnsupportedAddressing`) rather than risking stale SIDX data applied to a
changed resource. This is documented in `docs/DASH_SIDX_EVIDENCE.md`.
