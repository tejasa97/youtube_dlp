# Safari cookie conformance provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The deterministic corpus and expectations in
`internal/cookies/safari/parser_test.go` are derived from:

- `yt_dlp/cookies.py`: `_extract_safari_cookies`,
  `_parse_safari_cookies_header`, `_parse_safari_cookies_page`,
  `_parse_safari_cookies_record`, and `parse_safari_cookies`;
- `test/test_cookies.py::TestCookies::test_safari_cookie_parsing`.

The pinned one-cookie case retains the observable domain, name, path, encoded
value, secure flag, and expiration expectation. Additional multi-page,
resource-limit, malformed-framing, cancellation, filesystem, and fuzz cases
are independently constructed security evidence. No upstream source or
fixture is used at runtime or build time.
