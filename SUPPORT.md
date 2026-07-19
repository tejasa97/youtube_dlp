# Support

ytdlp-go is alpha software with a deliberately bounded compatibility corpus.
Before asking for help, read the [README](README.md), check the
[supported-extractor catalog](docs/SUPPORTED_SITES.md), and reproduce the
problem on the current `main` revision.

## Where to report

- Use a GitHub bug report for a reproducible defect in documented behavior.
- Use a site-support request for a new service or an unclaimed URL family.
- Use a feature request for a new CLI, API, protocol, or extension capability.
- Use the private process in [SECURITY.md](SECURITY.md) for vulnerabilities or
  reports that cannot be safely described without sensitive details.

There is no private end-user support channel for ordinary download failures,
and response times are not guaranteed. The security email is only for
vulnerability reports.

## What to include

Provide the revision, operating system and architecture, smallest reproducing
command, expected and actual result, and plain-text diagnostics. For site
problems, include a public example URL and the extractor name when known.

Do not include:

- cookies, tokens, passwords, authorization headers, or netrc files;
- browser profiles or operating-system credential-store exports;
- expiring signed media URLs or private response dumps;
- personal data, local usernames and paths, or private-media titles; or
- production signing keys or plugin trust material.

Replace sensitive values with clear placeholders rather than deleting the
surrounding diagnostic context. If a report cannot be reproduced without an
account, region, subscription, or ephemeral live state, say so; maintainers
may be unable to validate it.

## Scope expectations

A name in the extractor catalog means deterministic evidence exists for the
declared URL and response corpus. It does not guarantee every current page,
playlist, account state, region, or service response. Websites can change
without notice.

ytdlp-go does not provide DRM circumvention, help acquiring content without
authorization, or support for third-party applications that merely invoke the
binary. Reproduce wrapper or GUI problems directly with ytdlp-go before filing
them here.

See [Contributing](CONTRIBUTING.md) for the complete bug, feature, extractor,
fixture, and pull-request expectations.
