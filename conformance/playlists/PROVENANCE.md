# Playlist selection fixture provenance

`selection.expected.json`, `items.expected.json`, `flat.expected.json`, and
`random.expected.json` are
attributable, synthetic expectations derived
from the pinned yt-dlp reference checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The behavioral source is
`test/test_YoutubeDL.py::TestYoutubeDL::test_playlist_items_selection`
in `/Users/tejas/projects/yt-dlp-reference`. The upstream test establishes that
playlist start/end values are inclusive and one-based, reversal occurs after
selection, and each result retains its original source `playlist_index`.

The item-spec cases cover the pinned test's sparse indexes, legacy dash ranges,
colon ranges, positive and negative steps, negative indexes, infinity, zero,
and ordered duplicate suppression.

The flat-playlist expectation is derived from the pinned
`yt_dlp/options.py` definition of `--flat-playlist` and
`yt_dlp/YoutubeDL.py::YoutubeDL.process_ie_result`, which retain an unresolved
URL result inside a playlist instead of recursively extracting or downloading
it. The synthetic case combines that behavior with the pinned item-selection
and reverse ordering expectations. It records the retained URL-result type,
declared extractor key, and source `playlist_index`, with zero child
extractions and downloads. The pinned
`yt_dlp/YoutubeDL.py::YoutubeDL.__process_playlist` evaluates incomplete entry
filters and archive membership before processing each flat URL result; the Go
tests retain those policy boundaries without invoking the child extractor.

Random/lazy/error-policy behavior is derived from pinned
`yt_dlp/__init__.py` option conflict normalization and
`yt_dlp/YoutubeDL.py::YoutubeDL.__process_playlist`: randomization follows
selection, random takes precedence over reverse, lazy disables both ordering
transforms, ordinary entry failures can continue, and
`skip_playlist_after_errors` stops the remaining queue at its threshold. The
random fixture records a Go-injected deterministic permutation; upstream and
the CLI intentionally use nondeterministic system randomness.

The identifiers and compact JSON representations in this directory were
written specifically for this Go project. They do not copy service responses,
credentials, executable Python, or upstream implementation code. Production
and test execution do not access the reference checkout.
