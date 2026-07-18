# ARD Mediathek risk fixtures

Synthetic Page Gateway fixtures authored on 2026-07-18 from
`yt_dlp/extractor/ard.py` at upstream commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. IDs and media URLs are invented,
and no SSO token or audience data is present.

Known deviation: the optional ARD SSO JWT bootstrap is deliberately outside the
extractor request contract. Age-blocked (`blockedByFsk`) media therefore fails
closed with authentication-required. Item and collection APIs, live state,
regional failures, HLS/DASH/direct media, captions, and lazy bounded pagination
are implemented.
