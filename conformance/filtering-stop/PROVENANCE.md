# CLI filtering/stopping wave pinned corpus provenance

This fixture is a hand-authored, deterministic statement of behavior observed
from the read-only yt-dlp checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. The normative implementations are:

- simple field filters (`--match-title`, `--reject-title`, `--date`,
  `--dateafter`, `--datebefore`, `--min-views`, `--max-views`, `--age-limit`)
  in `yt_dlp/YoutubeDL.py`, `_match_entry` (lines 1578-1670), with the date
  grammar in `yt_dlp/utils/_utils.py`, `date_from_str`/`DateRange`
  (lines 1370-1465);
- stop conditions (`--break-on-existing`, `--break-on-reject`,
  `--break-per-input`) in `yt_dlp/YoutubeDL.py`, `_match_entry`,
  `__download_wrapper`, and `yt_dlp/__init__.py`, `report_conflict` and the
  `DownloadCancelled` handling (exit code 101 with
  "Aborting remaining downloads");
- `--max-downloads` counting in `yt_dlp/YoutubeDL.py`, `process_info`
  (`_num_downloads`, `check_max_downloads`);
- `--min-filesize`/`--max-filesize` enforcement in
  `yt_dlp/downloader/http.py` (lines 210-230) with the SIZE grammar of
  `utils.parse_bytes` (`lookup_unit_table`, strict);
- `--force-write-archive`, including its three aliases and its simulate/skip
  behavior, in `yt_dlp/options.py` (lines 1322-1329) and
  `yt_dlp/YoutubeDL.py` (`process_info` and `process_video_result`);
- the CLI flag definitions and conflict resolution in `yt_dlp/options.py`
  (lines 699-814) and `yt_dlp/__init__.py` (lines 309-321, 533-579, 885-906).

No Python executable is run by Go tests, builds, or the Docker image. The
fixture contains small attributable expectations, not copied upstream tests or
captured upstream output. Maintainers may reproduce an oracle investigation
only with the stated commit and a supported pinned interpreter; any resulting
fixture update must record its command, interpreter version, and date here.

## Known deviations

- Metadata actions (`--parse-metadata`/`--replace-in-metadata`) are applied
  after archive, simple, and generic filter selection, matching the pinned
  `_match_entry` before `pre_process` order. Downstream filename, format, and
  artifact stages retain the transformed metadata.
- `--force-write-archive`, `--force-write-download-archive`, and
  `--force-download-archive` record successfully selected simulate and
  skip-download entries, but never rejected, failed, or size-aborted entries.
- `--min-filesize`/`--max-filesize` are enforced only for direct HTTP media
  downloads (matching `downloader/http.py`); known lengths are rejected before
  transfer, unknown or misleading lengths are bounded during streaming, and
  size-aborted `.part` artifacts are removed (direct `--no-part` destinations
  are preserved). When the transport exposes `Content-Encoding`, the response
  retains the reference's size-check exemption. Subtitle and thumbnail
  payload writes never carry the bounds.
- `--match-title`/`--reject-title` patterns are compiled at option-validation
  time; the reference compiles at first evaluation. Invalid patterns fail
  closed with a sanitized local error in both products.
- A stopped run still returns the partial playlist/media result with
  `Stopped` set; the CLI emits the partial outputs, prints
  "Aborting remaining downloads", and exits 101 (unless `--break-per-input`).
- Simple-filter evaluation failures (bounded regex timeouts, oversized
  upload dates) propagate as typed errors so the entry fails closed; the
  reference raises or rejects through its own bounded evaluation.
