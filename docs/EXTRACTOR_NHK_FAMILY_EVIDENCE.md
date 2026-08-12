# NHK extractor family evidence

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` (`yt_dlp/extractor/nhk.py`).

## Extractors

| Go name | Reference class | Evidence |
| --- | --- | --- |
| `nhk_vod` | `NhkVodIE` | `TestNHKWorldVODExtract`, clip API URL (`TestNHKWorldVODClipAPIURL`), missing-stream, suitable/hostile routing |
| `nhk_vod_program` | `NhkVodProgramIE` | `TestNHKWorldProgramPlaylist`, precedence vs VOD, trailing-path rejection |
| `nhk_for_school_bangumi` | `NhkForSchoolBangumiIE` | version-ID replacement, chapters, exact bangumi/clip routes, credential-isolated Akamai HLS URL |
| `nhk_for_school_subject` | `NhkForSchoolSubjectIE` | allowlist, hostile child rejection, `subjectName` parsing (`TestNHKSchoolSougouSubjectTitle`) |
| `nhk_for_school_program_list` | `NhkForSchoolProgramListIE` | program.json parts → bangumi re-entry, strict JSON EOF |
| `nhk_radiru` | `NhkRadiruIE` | episode + playlist modes, news API (`TestNHKRadiruNewsPlaylistAndHeadline`), missing headline → unavailable |
| `nhk_radio_news_page` | `NhkRadioNewsPageIE` | `/radionews/` → `URLResult` targeting `nhk_radiru` (`TestNHKRadioNewsHandoff`) |
| `nhk_radiru_live` | `NhkRadiruLiveIE` | default/regional FM/R1, R2 national cross-area, unavailable NOA fallback, `--nhk-area` |

## Routing / transport / security

- Exact hosts only (`www3.nhk.or.jp`, `www2.nhk.or.jp`, `www.nhk.or.jp`, `api.nhkworld.jp`)
- Reject userinfo, ports, encoded separators (`%2f`, `%5c`, `%00`, `%2e`), hostname lookalikes, trailing path segments, `/radionews/extra`
- Radiru series API uses `corner_site_id` and top-level `episodes[]` with numeric `id` values
- Radiru extended program metadata uses `url_program_detail` and `aa_contents_id` (nonfatal on failure)
- API/config origins validated before fetch; CDN/media URLs use `strictValidHostedHTTPURL`
- API-derived manifest/media fetches require `CredentialIsolatedNoRedirectTransport`, fail closed with `ErrTransportIsolation`, mark `_credential_isolated` on emitted formats, and product downloads honor the flag via isolated transport
- Product dispatch keeps marked native HLS/DASH/HDS/ISM/direct downloads on that isolated transport and rejects external downloaders, YouTube live/post-live/SABR special paths, and HLS ffmpeg fallback before those paths can execute (`TestCredentialIsolated*`)
- The School product E2E traverses the real registry/client processing path and proves manifest/segment credential stripping, redirect refusal, categorized failure, and scratch cleanup (`TestNHKSchoolProductCredentialIsolation`)
- School/Radiru JSON parsers reject trailing values after the first object (`ensureJSONEOF`)
- Errors are categorized and secret-safe (`TestNHKSecretSafeErrors`)
- Context cancellation honored before network work

## Limits

Conservative bounds cover route URL bytes, response bytes, XML depth/tokens/attributes, JSON collections, playlist entries, thumbnails, categories/tags/cast, chapters, finite durations/dimensions, and metadata string lengths.

## Deviations / uncertainties

- CLI uses `--nhk-area` rather than yt-dlp `extractor_args nhkradirulive:area` (functionally equivalent knob)
- No DRM decryption
- Offline fixtures only; no live regional Japan success claim
- Some Radiru description-formatting flourishes from the JS timetable helper are intentionally simplified and bounded
- NHK World HLS master subtitle discovery is unsupported. The extractor exposes media formats only; it does not emit manifest-derived subtitle tracks or claim subtitle parity.

## Inventory classification

The extractor inventory classifies all eight NHK rows as `already_supported`.
