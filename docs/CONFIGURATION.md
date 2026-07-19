# Configuration

ytdlp-go implements a bounded subset of yt-dlp-style configuration discovery,
tokenization, aliases, and precedence. The same CLI parser consumes options
from configuration files and the command line, so unsupported options fail
instead of being silently ignored.

## Example

    # Lines beginning with # are comments
    --output-dir "~/Downloads"
    --output "%(title)s.%(ext)s"
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
- --paths home:PATH selects the home configuration/output path used by the CLI.

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
