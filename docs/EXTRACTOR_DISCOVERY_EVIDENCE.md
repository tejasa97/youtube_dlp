# Extractor discovery evidence

## Reference provenance

Behavior was inspected read-only against `yt-dlp/yt-dlp` at pinned commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The relevant reference paths are:

- `yt_dlp/options.py`: `--list-extractors` and
  `--extractor-descriptions` registration;
- `yt_dlp/__init__.py`: early `print_extractor_information` exit, URL
  association, and stdout behavior; and
- `yt_dlp/extractor/__init__.py` and `yt_dlp/extractor/common.py`: stable
  extractor ordering and description assembly.

The reference checkout was read as source text only. The Go product does not
read the checkout, execute Python, or use network data for discovery.

## Go behavior

`--list-extractors` and `--extractor-descriptions` are early, stdout-only
commands. They use the existing native product registry constructor catalog,
which remains the routing-priority authority. The display view is sorted by
extractor name with `generic` last.

`--list-extractors` consumes positional and batch-file URLs, deduplicates them
in input order, and prints each URL beneath every suitable display entry using
only native `Suitable` methods. URLs that match no concrete entry are printed
beneath `generic`. It does not call extraction, construct the normal runner,
or perform network I/O. With no inputs it prints names only.

`--extractor-descriptions` does not consume or inspect inputs. It prints the
canonical name when no description metadata is available and uses a bounded
one-line description when one is supplied. Optional metadata providers may
supply aliases through the typed API; aliases do not alter routing or
extractor selection and are not rendered by the CLI, matching the pinned
description format. The current built-in catalog has no selection aliases.
Generic is explicitly described as `Generic downloader that works on some
sites`.

Both commands write names, descriptions, and list URL examples to stdout;
diagnostics and writer failures go to stderr. Output checks context
cancellation before and between entries and returns the CLI cancellation
status. Simulation, skip-download, quiet, and download options do not change
discovery behavior.

Descriptions are UTF-8-safe, whitespace-normalized, one-line strings bounded
to 256 bytes. The catalog contains built-in extractors only; plugin discovery
and extractor selection are separate lanes.

## Known deviations

The pinned reference can report Python extractor working-state, search-prefix,
netrc, and extractor-specific description metadata. This native lane reports
only metadata attributable to the Go registry and intentionally omits those
unavailable fields. The command shape, stdout channel, URL deduplication and
association, ordering, generic placement, bounds, and cancellation contract
are covered.

## Automated evidence

- `internal/extractor.TestRegistryMetadataIsStableBoundedAndGenericLast`
- `internal/extractor.TestRegistryMetadataDoesNotDuplicateNames`
- `internal/extractor.TestRegistryMetadataForURLsUsesStableDisplayOrderAndGenericRemainder`
- `internal/extractor.TestRegistryMetadataForURLsRetainsOverlappingSuitableMatches`
- `pkg/ytdlp.TestBuiltInExtractorMetadataIsDeterministicAndOffline`
- `internal/cli.TestRunExtractorDiscoveryUsesOfflineSuitableMatchingAndStdout`
- `internal/cli.TestRunExtractorDescriptionsAreOfflineAndBounded`
- `internal/cli.TestWriteExtractorDiscoveryOmitsAliasesAndPropagatesWriterErrors`
- `internal/cli.TestRunExtractorDiscoveryCancellation`
