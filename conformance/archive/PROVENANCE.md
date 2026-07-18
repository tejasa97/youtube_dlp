# Download archive fixture provenance

The identity and matching expectations in this directory were derived from the
read-only upstream yt-dlp checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Relevant reference locations:

- `yt_dlp/utils/_utils.py`, `make_archive_id`: lowercase extractor key, one
  ASCII space, then the video ID;
- `yt_dlp/YoutubeDL.py`, `_make_archive_id`, `in_download_archive`, and
  `record_download_archive`: exact current/legacy ID matching and newline
  records;
- `yt_dlp/YoutubeDL.py`, `preload_download_archive`: UTF-8 line loading,
  surrounding-whitespace stripping, and duplicate collapse in the in-memory
  set.

The literal IDs are public synthetic examples. The duplicate deliberately
exercises idempotent migration. No fixture was generated at build or test time,
and neither Python nor the reference checkout is a dependency.

The Go implementation intentionally adds bounded record/file sizes, atomic
rewrite, stale-lock recovery, invalid UTF-8 rejection, and categorized errors.
Opaque nonempty legacy lines remain readable and are preserved during migration.
