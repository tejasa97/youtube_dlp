# Output artifact controls provenance

This track is compared with the pinned Python reference at
`/Users/tejas/projects/yt-dlp-reference`, commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The implementation is intentionally bounded and uses the existing Go output
template, path confinement, metadata, transaction, and cache layers. The
covered controls are:

- `--id` selects `%(id)s.%(ext)s` only when no explicit default `--output`
  template was supplied. Typed non-default templates do not suppress the media
  default, matching the pinned post-parse conflict rule.
- `--output-na-placeholder` is applied only by confined filename rendering;
  the default remains `NA` and the value is bounded and free of control lines.
- `--autonumber-start` and `--autonumber-size` are deterministic across nested
  playlist entries and multiple CLI inputs. Rejected, archived, and
  extractor-before-selection failures do not advance the counter; an accepted
  attempt remains counted if a later output step fails. `pre_process` and
  `after_filter` print stages use the pinned provisional, non-consuming value,
  while output filenames use the committed accepted count. `--break-per-input`
  resets that count for every top-level input. The public Go request carries the
  caller's prior zero-based count so concurrent clients do not share state.
- Info JSON cleaning removes the pinned private field set recursively and
  omits nulls. By default playlist metafiles retain `entries`, matching the
  reference's default playlist-metafile behavior; explicit `--clean-info-json`
  cleans playlist metafiles too, while `--no-clean-info-json` preserves all
  normalized fields.
- `--load-info-json` accepts one bounded single-video object only. It rejects
  playlists, path-bearing private fields, unsupported/userinfo URLs, malformed
  headers, credential fields, oversized/deep input, and symlink files. Its
  platform snapshot open does not follow the final path component, and opened
  handle/path identities are checked before and after the bounded read. Cookie,
  browser-cookie, netrc, and video-password request options are rejected at the
  API boundary so a loaded file cannot reuse ambient credentials. When a
  direct top-level `url` fails with a bounded network/internal/unsupported
  error before any artifact is committed, a distinct validated `webpage_url`
  is retried through the normal extractor path, matching the pinned
  `download_with_info_file` fallback at a narrower error boundary.
- `--rm-cache-dir` removes only an explicitly configured cache-named root using
  descriptor-bounded native cache operations. The root itself must not be a
  symlink, broad filesystem root, or home directory. Unknown entries,
  root-level regular files (including unknown `.cache-*` names), links, and
  special files fail closed; context cancellation stops before further
  removal. Unix-like builds coordinate cache users with a shared process gate
  and an advisory root lock; Windows rejects destructive cleanup rather than
  claiming an equivalent no-follow boundary.

Evidence is provided by:

- `pkg/ytdlp.TestOutputArtifactTemplatePlaceholderAndAutonumberCompatibility`
- `pkg/ytdlp.TestInfoJSONCleaningPreservesPublicMetadataAndExplicitOverride`
- `pkg/ytdlp.TestLoadInfoJSONDownloadsBoundedMetadataWithoutAmbientCredentials`
- `pkg/ytdlp.TestLoadInfoJSONPreservesAcceptedAutonumberOnOutputFailure`
- `pkg/ytdlp.TestLoadInfoJSONArchiveIdentityUsesExtractorMetadata`
- `pkg/ytdlp.TestLoadInfoJSONFallsBackFromDirectURLToWebpage`
- `pkg/ytdlp.TestLoadInfoJSONRejectsUnsafeShapesBoundsAndCancellation`
- `pkg/ytdlp.TestAutonumberCountsOnlyAcceptedPlaylistEntries`
- `internal/cache.TestRemoveRootIsConfinedAtomicAndCancellationAware`
- `internal/cache.TestRemoveRootRejectsUnknownRootLevelTemporaryFile`
- `internal/cli.TestOutputArtifactFlagsAndIDOrdering`
- `internal/cli.TestLoadInfoJSONAndRemoveCacheCLIRequestsAreURLIndependent`
- `internal/cli.TestCLIPropagatesAutonumberAcrossMultipleInputs`

No Python code, network fixture, or real credential was copied into the
repository. Playlist `chapter`, `annotation`, and `pl_video` output types,
arbitrary executable metadata loading, and cache removal of custom roots whose
names do not identify a cache remain deliberately outside this claim.
