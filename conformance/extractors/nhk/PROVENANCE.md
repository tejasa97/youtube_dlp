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
- NHK World clip API path `video_clips/{id}` (no doubled suffix)
- School bangumi quoted `var` / `programObj` assignments, `chapterTime.push`, `cpTitle` HTML
- School subject `subjectName` span and program.json `part[*].part-video-dasid`
- Radiru series JSON `episodes[]` at top level with numeric `id`, `aa_contents_id`, and `program_title`
- Radiru `url_program_detail` template with `{broadcastEventId}` and extended program detail JSON
- Radiru news JSON `main.detail_list[]` and `config_web.xml` `url_program_noa` / area HLS blocks

Minimization / anonymization:

- Descriptions are short synthetic English/Japanese placeholders
- Stream URLs point at deterministic public CDN-shaped hosts under fixture control
- No copyrighted long-form descriptions or live payloads were copied
- Fixtures were authored by hand from the reference structure; Python was not executed

Synthetic values:

- Episode/program IDs and titles
- HLS master playlists with synthetic CDN URLs
- Radiru config areas `tokyo` / `sapporo` / `fukuoka`, news `all.json`, and now-on-air JSON for area keys `130` / `010` / `800`

Known deviations:

- `--nhk-area` / `NHKOptions.RadiruArea` instead of `extractor_args nhkradirulive:area`
- Offline fixtures only; no live Japan geo canary
- No DRM decryption path
- Radiru extended program-detail metadata failure is nonfatal as in the reference, but description formatting helpers are intentionally simpler/bounded
- Credential-isolated manifest/media transport is required for emitted formats; product format selection propagates `_credential_isolated` into native HLS/DASH/HDS/ISM/direct downloads and fails closed before external, YouTube-special, or HLS ffmpeg-fallback dispatch
- NHK World HLS master subtitle discovery is explicitly outside this change: no subtitle tracks or subtitle-parity claim are produced from those manifests
