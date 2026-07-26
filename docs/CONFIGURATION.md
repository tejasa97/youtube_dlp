# Configuration

ytdlp-go implements a bounded subset of yt-dlp-style configuration discovery,
tokenization, aliases, and precedence. The same CLI parser consumes options
from configuration files and the command line, so unsupported options fail
instead of being silently ignored.

## Example

    # Lines beginning with # are comments
    --output-dir "~/Downloads"
    --paths "subtitle:captions"
    --paths "thumbnail:images"
    --paths "infojson:metadata"
    --write-thumbnail
    --convert-thumbnails "webp>png/jpg"
    --output "%(title)s.%(ext)s"
    --output "subtitle:%(title)s.%(ext)s"
    --output "infojson:%(title)s.%(ext)s"
    --format "bestvideo+bestaudio/best"
    --retries 3
    --concurrent-fragments 4

POSIX-style single quotes, double quotes, backslash escaping, comments, empty
arguments, and line continuations are supported. Diagnostics retain source
file, line, and column information.

## Discovery and precedence

Sources are applied from lowest to highest precedence:

1. system configuration;
2. user configuration;
3. the home path selected with --paths home:PATH;
4. portable yt-dlp.conf beside the executable;
5. explicitly loaded configuration locations;
6. command-line arguments.

`--output` is repeatable. A `TYPE:TEMPLATE` value configures `default`,
`subtitle`, `thumbnail`, `description`, `infojson`, `link`,
`pl_description`, `pl_infojson`, or `pl_thumbnail`; comma-separated types may share one template. Later values
replace earlier values for the same type, so command-line typed templates
override configuration-file values without clearing unrelated types.

`--convert-thumbnails` accepts `jpg`, `png`, `webp`, or an ordered mapping such
as `webp>png/jpg`. A later command-line value replaces the configured mapping;
`--convert-thumbnails none` restores the no-conversion default.

`--paths` is also repeatable. An untyped value or `home:PATH` selects the
common output root. The supported typed values are `subtitle`, `thumbnail`,
`description`, `infojson`, `link`, `pl_description`, `pl_infojson`, and
`pl_thumbnail`; comma-separated types may share one directory. Typed paths are
relative children confined beneath `home`, and unspecified types fall back to
`home`. Later values replace earlier values for the same type. The public Go
API exposes the same mapping as `OutputPaths`. A later `TYPE:` or `TYPE:.`
clears an inherited typed directory and restores home fallback.

Only the first existing candidate in each default group is loaded. The user
group follows the platform path convention:

- Unix-like systems: $XDG_CONFIG_HOME/yt-dlp, ~/.config/yt-dlp, and
  ~/.yt-dlp candidates;
- Windows: %APPDATA%\yt-dlp and the corresponding home-directory candidates;
- system Unix-like configuration: /etc/yt-dlp candidates;
- portable configuration: yt-dlp.conf beside the executable.

Candidate filenames include yt-dlp.conf and, where applicable, config,
config.txt, or yt-dlp.conf.txt. Use --config-location PATH when exact,
cross-platform behavior matters. A directory location resolves to its
yt-dlp.conf file.

Explicit locations declared inside another configuration are resolved relative
to the declaring file. Included files have lower precedence than the source
that includes them. Duplicate canonical files are loaded once.

## Control options

- --config-location PATH and --config-locations PATH load a file, a directory
  containing yt-dlp.conf, or stdin when PATH is -.
- --ignore-config and --no-config skip default discovery.
- --no-config-locations clears inherited explicit locations.
- --paths [TYPES:]PATH selects the home or a supported typed output path.
  `temp`, `chapter`, `annotation`, and `pl_video` are rejected until their
  corresponding artifact lifecycle exists.

## Encodings

UTF-8 is the default. UTF-8, UTF-16, and UTF-32 byte-order marks are honored.
An initial coding declaration may select ASCII, Latin-1, or Windows-1252:

    # coding: windows-1252

Unsupported declarations and malformed byte sequences are categorized errors.

## Aliases

Dynamic aliases use the yt-dlp-compatible principal form:

    --alias audio "-f {0} -x"
    --audio "bestaudio"

Alias placeholders range from {0} through {99}. Expansion count, token count,
token size, file count, include depth, and total file bytes are bounded.
Recursive or malformed aliases fail explicitly.

Preset alias parsing exists for the declared compatibility corpus, but a preset
that expands to a CLI option not exposed by this executable will still fail at
the CLI boundary. Run ytdlp-go --help before relying on an upstream preset.

## Security behavior

Configuration reads are context-cancellable and size-bounded. Paths are
canonicalized, non-regular locations are rejected, recursion is bounded, and
errors do not render conventional secret values. Configuration files may still
contain sensitive arguments; protect them with appropriate filesystem
permissions and never attach a real credential-bearing file to a public issue.

Deterministic precedence, encoding, alias, cancellation, and hostile-input
evidence is tracked by the compat.configuration capability in
conformance/parity_manifest.yaml.
