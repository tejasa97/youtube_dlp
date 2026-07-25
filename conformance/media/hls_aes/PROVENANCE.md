# HLS AES-128 initialization-map provenance

The encrypted initialization-map playlists and media bytes embedded in
`internal/protocol/hls` tests are deterministic and synthetic. Keys, IVs,
plaintext, URLs, and ciphertext are invented in the tests; no captured media,
credentials, account data, or Python runtime is used.

Behavior is derived from the read-only yt-dlp reference checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/downloader/hls.py` processes `EXT-X-KEY` state in playlist order;
- the same file attaches the current decryption state to an `EXT-X-MAP`
  initialization fragment when that tag is encountered; and
- `yt_dlp/downloader/fragment.py` applies the attached AES-128 key and IV while
  downloading the fragment.

The Go parser likewise snapshots the active key when an `EXT-X-MAP` is
declared, so a later key rotation affects media segments without retroactively
changing the map. Consecutive map de-duplication includes URL, byte range, key
URL, and IV; an A→B→A transition re-emits A, while the same resource under a
distinct encryption identity is not silently reused. Keys are fetched only for
retained media and are cached by URI within one download. In accordance with
HLS map semantics, AES-128 initialization maps require an explicit IV; ordinary
media segments continue to support sequence-derived implicit IVs.

SAMPLE-AES, DRM key formats, and non-AES-128 encryption remain unsupported.
