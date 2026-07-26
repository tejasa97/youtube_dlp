# PRX provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/prx.py`.

Implemented: `PRXStoryIE`, `PRXSeriesIE`, and `PRXAccountIE`. The Go port uses
the pinned CMS API base, canonical beta PRX URL results, `page=1&per=100`
pagination, and the documented embedded image/account/series/audio/items
relations. Fixture tests cover route rejection, single audio, status mapping,
and empty lazy pagination. Optional malformed playlist cards are skipped;
requested identities, images, and media URLs fail closed.

Deliberate deviations: `PRXStoriesSearchIE` and `PRXSeriesSearchIE` are deferred.
No live API is consulted by tests, so API-schema drift remains a live-service
risk. Multipart entries retain stable part identifiers as URL results in this
port's lazy-entry model; their audio is resolved by the canonical story URL.
