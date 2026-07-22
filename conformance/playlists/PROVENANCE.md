# Playlist selection fixture provenance

`selection.expected.json` and `items.expected.json` are attributable,
synthetic expectations derived
from the pinned yt-dlp reference checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The behavioral source is
`test/test_YoutubeDL.py::TestYoutubeDL::test_playlist_items_selection`
in `/Users/tejas/projects/yt-dlp-reference`. The upstream test establishes that
playlist start/end values are inclusive and one-based, reversal occurs after
selection, and each result retains its original source `playlist_index`.

The item-spec cases cover the pinned test's sparse indexes, legacy dash ranges,
colon ranges, positive and negative steps, negative indexes, infinity, zero,
and ordered duplicate suppression. The identifiers and compact JSON
representation in this directory were written specifically for this Go
project. They do not copy service responses, credentials, executable Python,
or upstream implementation code. Production and test execution do not access
the reference checkout.
