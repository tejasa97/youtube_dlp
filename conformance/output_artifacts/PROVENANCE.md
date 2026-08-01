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
  playlist entries and multiple CLI inputs. Rejected entries consume a slot,
  matching the reference's video-count lifecycle. The public Go request carries
  the caller's prior zero-based count so concurrent clients do not share state.
- Info JSON cleaning removes the pinned private field set recursively and
  omits nulls. By default playlist metafiles retain `entries`, matching the
  reference's default playlist-metafile behavior; explicit `--clean-info-json`
  cleans playlist metafiles too, while `--no-clean-info-json` preserves all
  normalized fields.
- `--load-info-json` accepts one bounded single-video object only. It rejects
  playlists, path-bearing private fields, unsupported/userinfo URLs, malformed
  headers, credential fields, oversized/deep input, and symlink files. Cookie,
  browser-cookie, netrc, and video-password request options are rejected at the
  API boundary so a loaded file cannot reuse ambient credentials.
- `--rm-cache-dir` removes only an explicitly configured cache-named root using
  the native cache namespace format. The root itself must not be a symlink,
  broad filesystem root, or home directory. Unknown entries, links, and
  special files fail closed; context cancellation stops before further removal.

Evidence is provided by:

- `pkg/ytdlp.TestOutputArtifactTemplatePlaceholderAndAutonumberCompatibility`
- `pkg/ytdlp.TestInfoJSONCleaningPreservesPublicMetadataAndExplicitOverride`
- `pkg/ytdlp.TestLoadInfoJSONDownloadsBoundedMetadataWithoutAmbientCredentials`
- `pkg/ytdlp.TestLoadInfoJSONRejectsUnsafeShapesBoundsAndCancellation`
- `pkg/ytdlp.TestAutonumberTracksPlaylistEntriesAndRejectedMedia`
- `internal/cache.TestRemoveRootIsConfinedAtomicAndCancellationAware`
- `internal/cli.TestOutputArtifactFlagsAndIDOrdering`
- `internal/cli.TestLoadInfoJSONAndRemoveCacheCLIRequestsAreURLIndependent`
- `internal/cli.TestCLIPropagatesAutonumberAcrossMultipleInputs`

No Python code, network fixture, or real credential was copied into the
repository. Playlist `chapter`, `annotation`, and `pl_video` output types,
arbitrary executable metadata loading, and cache removal of custom roots whose
names do not identify a cache remain deliberately outside this claim.
