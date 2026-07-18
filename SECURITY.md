# Security Policy

## Supported versions

This project is pre-release software. Security fixes are made on the current
`main` branch; no released version has a long-term support commitment yet.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
**Report a vulnerability** feature on the repository's Security tab. If that
feature is unavailable, email `tejasarlimatti@gmail.com` with the subject
`ytdlp-go security report`.

Include the affected revision, impact, reproduction steps, and any suggested
remediation. Use synthetic data wherever possible. Do not send real account
cookies, access tokens, private media, personal data, or production signing
keys. If sensitive evidence is unavoidable, first ask for a safe transfer
method.

You should receive an acknowledgement within seven days. The project will
attempt to validate the report, coordinate a fix and disclosure timeline, and
credit the reporter when requested. This is not a bug-bounty program, and no
payment or safe-harbor promise is currently offered. Testing must comply with
applicable law and must not disrupt third-party services or access data without
authorization.

## Scope notes

The current security boundary and known residual risks are documented in
[`docs/P2_SECURITY_REVIEW.md`](docs/P2_SECURITY_REVIEW.md). Test signing keys,
cookie values, identifiers, and credentials in `conformance/` are deterministic
non-production fixtures; they must never be promoted to production authority.
