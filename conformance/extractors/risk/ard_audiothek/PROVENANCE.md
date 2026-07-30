# ARD Audiothek risk fixtures

Synthetic GraphQL fixtures authored on 2026-07-30 from
`yt_dlp/extractor/ard.py` at upstream commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. URNs, stream URLs, and show
metadata are invented, and no SSO token or audience data is present.

Known deviation: the optional ARD Mediathek SSO JWT bootstrap is deliberately
outside the extractor request contract. Public Audiothek episode and bounded
show playlist APIs use credential-isolated exact-origin no-redirect GraphQL
requests with one eager single-fetch response per extraction. Episode audio
streams are marked credential-isolated for download.
