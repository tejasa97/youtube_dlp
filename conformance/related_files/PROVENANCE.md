# Related-file provenance

The expected option behavior and shortcut payload shapes were derived from the
read-only yt-dlp checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Relevant pinned sources:

- `yt_dlp/options.py`: filesystem and internet-shortcut options;
- `yt_dlp/YoutubeDL.py`: video and playlist write ordering, overwrite behavior,
  final playlist JSON refresh, URL validation boundary, and link dispatch;
- `yt_dlp/utils/_utils.py`: `.url`, `.webloc`, and `.desktop` templates and
  desktop locale-string escaping.

Tests use only repository-authored synthetic metadata and local HTTP fixtures.
No upstream code or live response is copied into production, build, or test
execution, and Python is not invoked.
