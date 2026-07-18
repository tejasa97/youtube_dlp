# Instagram risk fixtures

These synthetic fixtures were authored for the Go port on 2026-07-18 from the
field names and control flow in `yt_dlp/extractor/instagram.py` at upstream
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. They contain reserved example
domains and invented identifiers; they were not captured from a user session
and contain no cookies, tokens, or personal data.

Known deviation: logged-out post extraction consumes bounded media JSON already
embedded in the profiled webpage instead of reproducing Instagram's volatile
LSD/GraphQL bootstrap. Stories fail closed as authentication-required when no
media is present. Inline MPD XML is not expanded; direct video versions remain
usable. Profile pagination uses the public web-profile/timeline shapes and is
lazy and cursor-bounded.
