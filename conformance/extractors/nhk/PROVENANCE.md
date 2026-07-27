# NHK family fixture provenance

Pinned behavioral reference:

- Repository: yt-dlp/yt-dlp
- Commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Primary module: `yt_dlp/extractor/nhk.py`

Referenced classes / functions:

- `NhkVodIE`, `NhkVodProgramIE`
- `NhkForSchoolBangumiIE`, `NhkForSchoolSubjectIE`, `NhkForSchoolProgramListIE`
- `NhkRadiruIE`, `NhkRadioNewsPageIE`, `NhkRadiruLiveIE`
- Helpers: `_extract_episode_info`, `_call_api`, school version-ID replacement, Radiru config XML area/`{ch}hls` resolution

Retained shapes:

- NHK World showsapi v1 episode/program JSON (`id`, `title`, `video`/`audio.url`, categories/tags/images)
- School bangumi quoted `var` / `programObj` assignments, `chapterTime.push`, `cpTitle` HTML
- School subject allowlist and program.json `part[*].part-video-dasid`
- Radiru series JSON `main.episodes[]` and `config_web.xml` `<data>/<area>/<areakey>/<r1hls|r2hls|fmhls>`

Minimization / anonymization:

- Descriptions are short synthetic English/Japanese placeholders
- Stream URLs point at deterministic public CDN-shaped hosts under fixture control
- No copyrighted long-form descriptions or live payloads were copied
- Fixtures were authored by hand from the reference structure; Python was not executed

Synthetic values:

- Episode/program IDs and titles
- HLS master playlists with synthetic CDN URLs
- Radiru config areas `tokyo` / `sapporo` and HLS endpoints

Known deviations:

- `--nhk-area` / `NHKOptions.RadiruArea` instead of `extractor_args nhkradirulive:area`
- Offline fixtures only; no live Japan geo canary
- No DRM decryption path
- Radiru extended program-detail metadata failure is nonfatal as in the reference, but description formatting helpers are intentionally simpler/bounded
