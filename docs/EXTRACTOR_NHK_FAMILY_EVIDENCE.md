# NHK extractor family evidence

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` (`yt_dlp/extractor/nhk.py`).

## Extractors

| Go name | Reference class | Evidence |
| --- | --- | --- |
| `nhk_vod` | `NhkVodIE` | `TestNHKWorldVODExtract`, clip API URL (`TestNHKWorldVODClipAPIURL`), missing-stream, suitable/hostile routing |
| `nhk_vod_program` | `NhkVodProgramIE` | `TestNHKWorldProgramPlaylist`, precedence vs VOD, trailing-path rejection |
| `nhk_for_school_bangumi` | `NhkForSchoolBangumiIE` | version-ID replacement, chapters, Akamai HLS URL |
| `nhk_for_school_subject` | `NhkForSchoolSubjectIE` | allowlist, hostile child rejection, `subjectName` parsing (`TestNHKSchoolSougouSubjectTitle`) |
| `nhk_for_school_program_list` | `NhkForSchoolProgramListIE` | program.json parts → bangumi re-entry, strict JSON EOF |
| `nhk_radiru` | `NhkRadiruIE` | episode + playlist modes, news API (`TestNHKRadiruNewsPlaylistAndHeadline`), missing headline → unavailable |
| `nhk_radio_news_page` | `NhkRadioNewsPageIE` | `/radionews/` → `URLResult` targeting `nhk_radiru` (`TestNHKRadioNewsHandoff`) |
| `nhk_radiru_live` | `NhkRadiruLiveIE` | default/regional FM/R1, R2 national cross-area, unavailable NOA fallback, `--nhk-area` |

## Routing / transport / security

- Exact hosts only (`www3.nhk.or.jp`, `www2.nhk.or.jp`, `www.nhk.or.jp`, `api.nhkworld.jp`)
- Reject userinfo, ports, encoded separators (`%2f`, `%5c`, `%00`, `%2e`), hostname lookalikes, trailing path segments, `/radionews/extra`
- Episode clips call `.../video_clips/{id}` without a doubled `/video_clips` suffix
- API/config origins validated before fetch; CDN/media URLs use `strictValidHostedHTTPURL`
- API-derived manifest/media fetches require `CredentialIsolatedNoRedirectTransport`, fail closed with `ErrTransportIsolation`, and mark `_credential_isolated` on emitted formats
- School/Radiru JSON parsers reject trailing values after the first object (`ensureJSONEOF`)
- Errors are categorized and secret-safe (`TestNHKSecretSafeErrors`)
- Context cancellation honored before network work

## Limits

Conservative bounds cover response bytes, XML depth/tokens, JSON collections, playlist entries, thumbnails, categories/tags/cast, chapters, and metadata string lengths.

## Deviations / uncertainties

- CLI uses `--nhk-area` rather than yt-dlp `extractor_args nhkradirulive:area` (functionally equivalent knob)
- No DRM decryption
- Offline fixtures only; no live regional Japan success claim
- Some Radiru description-formatting flourishes from the JS timetable helper are intentionally simplified and bounded
- HLS subtitle discovery for NHK World masters is not claimed beyond format exposure in the current fixture corpus

## Checklist impact

Promoted exactly eight NHK rows from `requires_new_backend` to `already_supported`.

- `already_supported`: 139 → 147
- `requires_new_backend`: 1,174 → 1,166
