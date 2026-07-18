# Downloader conformance provenance

The deterministic retry/backoff, rate-limit, file-retry, fragment-retry, ISM,
and external-downloader expectations in this lane were derived by inspection
only from the read-only `yt-dlp` checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/downloader/common.py` (rate-limit and file retry behavior);
- `yt_dlp/downloader/http.py` (throttling and retry behavior);
- `yt_dlp/downloader/fragment.py` (fragment retry behavior);
- `yt_dlp/downloader/ism.py` (Smooth Streaming fragment ordering); and
- `yt_dlp/downloader/external.py` (external downloader dispatch concept).

Fixtures are synthetic and deterministic. This Go implementation imports no
code, runtime, or build dependency from that checkout or from Python.
