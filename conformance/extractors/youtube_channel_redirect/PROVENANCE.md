# YouTube conditional channel redirect fixture provenance

The deterministic page in this directory derives from the non-HTTP regional
channel redirect handled by `YoutubeTabIE._real_extract` in the read-only
reference checkout
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/youtube/_tab.py:2294-2304`.

It preserves the attributable
`onResponseReceivedActions[].navigateAction.endpoint.commandMetadata.webCommandMetadata.url`
shape while replacing every channel identity and all page content with
synthetic values. It proves that a bare regional destination inherits the
caller's explicit tab, or remains bare when the caller requested all uploads.

Production and tests neither import nor execute the reference checkout or
Python.
