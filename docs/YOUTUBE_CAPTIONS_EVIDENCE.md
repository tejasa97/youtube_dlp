# Native YouTube captions evidence

The Go extractor implements the caption-track behavior derived from the pinned
read-only reference `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/extractor/youtube/_video.py` (`_SUBTITLE_FORMATS` and the
  `playerCaptionsTracklistRenderer` traversal);
- `yt_dlp/extractor/youtube/_base.py` (client PO-token policies);
- `yt_dlp/extractor/youtube/pot/provider.py` (`subs` context).

Manual tracks are exposed under `subtitles`; `kind=asr` tracks are exposed
under `automatic_captions`. Every accepted language receives `json3`, `srv1`,
`srv2`, `srv3`, `ttml`, `srt`, and `vtt` URLs in stable order. Existing query
fields are preserved, `fmt` is replaced, and `xosf` is removed. Automatic
caption translations and original-language aliases are normalized from the
bounded translation-language renderer. Translated manual captions are produced
only when the embedding request sets `YouTubeTranslatedCaptions`.

Caption base URLs must be HTTPS YouTube `/api/timedtext` URLs without
credentials, ports, fragments, or encoded paths. Player, track, translation,
text, URL, and query dimensions are bounded. Duplicate language/URL entries
across player clients are removed deterministically.

Tracks carrying the pinned `xpe` or `xpv` experiment marker require a native
`subs` PO token. At most one token request is made for each client/video binding;
the token is applied as `pot`, `potc=1`, and `c=<client>`. A required-token miss
skips only that client's captions, allowing playable media and captions from
other clients to survive. Cancellation aborts the operation, while provider
failure text and token values are excluded from public errors.

The same policy evidence restores the pinned Android exception: a successful
player token can make its separate GVS token optional. Required requests under
the explicit `never` fetch policy now return the documented unavailable result
instead of silently looking successful.

## Explicit deviations

- Caption metadata is extracted, but the project does not yet expose yt-dlp's
  full CLI subtitle-selection and write flags.
- Authenticated/Premium client policy, live-chat captions, and caption renderer
  variants outside `playerCaptionsTracklistRenderer` remain pending.
- No built-in PO-token generator or Python fallback is introduced.

All deterministic fixtures use artificial renderer data and inert token values.
They emit synthetic caption URLs but perform no live YouTube caption request.
