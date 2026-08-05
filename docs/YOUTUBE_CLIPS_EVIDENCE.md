# YouTube Clips integration evidence

Status: implemented in PR5 (YouTube Clips via transparent source re-entry).

## Reference

Pinned Python reference: `yt-dlp/yt-dlp` at commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

- `yt_dlp/extractor/youtube/_clip.py:1-11` — clip URL and clip-id grammar.
- `yt_dlp/extractor/youtube/_clip.py:28-45` — source video id and loop timing traversal.
- `yt_dlp/extractor/youtube/_clip.py:47-54` — url_transparent result with clip id precedence,
  `media_type: clip`, `section_start`/`section_end`, and https-priority `_format_sort_fields`.
- `yt_dlp/YoutubeDL.py:1978-1991` — id/extractor overlay exemption for url_transparent results
  carrying section fields.

## Design

PR5 builds on PR3 (bounded `ytInitialData` watch metadata) and PR4 (generic media-section
consumer). It adds a `codex/youtube-clips` extractor layer that:

- Routes `/clip/<id>` on standard youtube.com hosts (`www`, root, `m.`) at the `Extract`
  boundary, before `parseYouTubeTarget`, so clip ids (which are not 11-char video ids) never
  pass through the `youtubeIDPattern` validator. nocookie hosts are rejected.
- Bounded-parses the clip page `ytInitialData` for `currentVideoEndpoint.watchEndpoint.videoId`
  (fails closed with the pinned "Unable to find video ID") and the deep
  `engagementPanels → … → loopCommand → startTimeMs/endTimeMs` chain (bounded walk; missing or
  malformed timing fails closed).
- Re-enters the existing YouTube video extractor via a transparent URL rewrite, then overlays
  the clip identity: clip `id` wins (per the pinned id-overlay rule), `media_type: clip`,
  `section_start`/`section_end` (ms/1000, validated), the clip webpage URL, and the
  https-priority `_format_sort_fields` for ffmpeg-delegable format selection.

## Behavior

- Source title, description, channel, formats, and other metadata remain authoritative; only
  the clip identity fields are overlaid.
- PR4's generic section consumer reads the overlaid `section_start`/`section_end` to derive the
  clip duration and produce a sectioned artifact named by the clip id.
- Malformed source ids, hostile URLs (userinfo, ports, encoded separators, lookalike hosts),
  and missing/over-budget timing fail closed before any output mutation.

## Deferred (documented)

- Live-clip and authenticated clip surfaces beyond the existing boundaries.
- Upstream's `FIXME` acknowledging that clip-local metadata (distinct from the source video)
  is not separately extracted; this follows the pinned transparent behavior.

## Validation

- Extractor unit tests (clip-id recognition, bounded parse, timing validation, overlay,
  hostile-URL rejection) and the transparent re-entry integration test.
- Full-clip product E2E: synthetic clip page → source watch page → format selection → PR4
  section download → clipped artifact carrying clip id and `media_type: clip`.
- `go test ./...`, vet, tidy, diff-check, paritycheck, and cross-builds.
