# YouTube subtitle CLI evidence

The product can now select and download the manual and automatic caption URLs
already exposed by the native YouTube extractor. The selection behavior is
derived from `YoutubeDL.process_subtitles` and the subtitle tests in
`test/test_YoutubeDL.py` at the pinned read-only reference commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Supported command-line options are:

- `--write-subs` / `--write-srt` and their negative aliases;
- `--write-auto-subs` / `--write-automatic-subs` and their negative aliases;
- repeatable, comma-separated `--sub-langs` / `--srt-langs` rules; and
- slash-separated `--sub-format` preferences, including `best` fallback.

The legacy `--all-subs` alias selects every language when either subtitle
write option is enabled.

Manual subtitles take precedence when the same language also has an automatic
caption. With no language rule, selection prefers English manual subtitles,
then English automatic captions, then the first manual or automatic language.
Language rules preserve order, support `all`, ordered exclusions such as
`all,-en`, and bounded case-insensitive RE2 regular expressions. Format
selection uses the first preference with a matching extension and otherwise
falls back to the extractor's final (best) entry.

Sidecars use the confined output template and the conventional
`NAME.LANGUAGE.EXTENSION` filename. They are downloaded atomically through the
native downloader with validated global/per-track HTTP headers, retries, rate
limits, cancellation, overwrite policy, URL redaction, and a 16 MiB hard
ceiling. `--skip-download` skips media while still writing requested subtitle
sidecars. Public results expose subtitle
artifacts and add the selected `_auto`, `filepath`, URL, and extension under
`requested_subtitles`.

Deterministic coverage uses only synthetic local HTTP responses. The ordered
selection expectations are attributable to the pinned reference tests; no
reference runtime, production caption URL, account, cookie, or token is used.

## Explicit deviations

- Go uses bounded RE2 expressions rather than Python's wider regular-expression
  grammar.
- `--list-subs`, CLI subtitle conversion/embedding, live chat, and inline
  extractor-provided subtitle `data` remain outside this sidecar wave. Typed Go
  post-processing APIs already support subtitle conversion and embedding.
- Subtitle sidecars always use the native downloader; external downloader
  argument conventions are not inferred for them.
- Consistent with this port's fail-closed output policy, an existing sidecar is
  an error unless overwrite is enabled; upstream treats it as an already
  completed subtitle.

Required evidence includes `TestSubtitleSidecarsDownloadWithSkipDownload`,
`TestSubtitleSelectionMatchesPinnedReferenceCases`,
`TestSubtitleFormatSelectionFailsBeforeSidecarWrite`,
`TestSubtitleLiteralOutputSuffixIsPreserved`,
`TestSubtitleHeadersValidateOnlySelectedTrack`,
`TestSelectSubtitleLanguagesOrderedRules`,
`TestSubtitleDestinationExistingFileFailsClosed`,
`TestSubtitleDownloadCancellation`,
`TestSubtitleOptionsRejectInvalidRegexBeforeNetwork`,
`TestSubtitleMetadataCombinedLanguageLimit`,
`FuzzValidateSubtitleOptions`, and
`TestRunWritesSelectedSubtitlesWhileSkippingMedia`.
