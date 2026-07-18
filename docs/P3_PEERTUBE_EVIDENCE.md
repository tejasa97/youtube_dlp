# Phase 3 PeerTube extractor evidence

The native extractor covers federated single-video metadata, direct files, HLS
playlists, playlist-contained direct files, optional full descriptions,
captions, thumbnails, uploader/channel metadata, counts, tags, categories,
language, licence, age rating, and explicit live state.

Routing is intentionally more conservative than the pinned reference's large
generated instance list. Normal HTTP(S) URLs are accepted only for a small set
of established instances or hosts with a `peertube*` DNS label. Any other
federated instance remains expressible through the explicit
`peertube:host:id` form. Generic-page PeerTube detection and account/channel/
playlist extraction are not claimed by this increment.
Password-protected video success is also not claimed until the shared request
contract has an explicit secret-handle field; authentication-required responses
are nevertheless categorized without exposing provider response text.

The explicit form accepts only bounded DNS hosts and rejects credentials,
ports, IP literals, localhost, `.local`, and `.internal` names. Media assets are
limited to bounded HTTP(S) URLs with public-style DNS hosts and no credentials,
ports, fragments, or network-path references. API collections have fixed hard
limits. These checks reduce accidental SSRF and unbounded-allocation exposure;
DNS rebinding protection remains the responsibility of the shared network
transport.

HTTP 401 is authentication-required, 403 with a geo marker and 451 are regional
restriction, 403 otherwise is authentication-required, 404/410 are unavailable,
malformed or excessive JSON is invalid metadata, and other HTTP/transport
failures use `ErrPeerTubeNetwork`. Optional caption and full-description
failures do not discard otherwise usable video metadata. Context cancellation
always terminates extraction.
