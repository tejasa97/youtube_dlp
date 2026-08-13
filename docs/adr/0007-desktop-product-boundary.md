# ADR 0007: Desktop boundary through explicit provider composition

Status: Accepted

## Context

The engine supports many provider families. VidStow is a separate focused
desktop application that intentionally exposes a smaller product surface. A
runtime allowlist would narrow routing only after broad provider code had been
linked and would create another hidden policy mode.

The product boundary is therefore established through ordinary, explicit Go
dependencies rather than global registration or a runtime profile.

## Decision

The media engine defines provider-neutral contracts and receives providers
explicitly from its composition root. It does not discover providers through
global initialization, blank imports, package-level registration, or a runtime
extractor allowlist.

There are two independent compositions:

- the broad CLI and `pkg/ytdlp` compatibility facade compose the full provider
  catalog; and
- VidStow composes root `engine` with the public YouTube provider.

The YouTube provider contains the related implementation needed by that
provider family. This does not expose all YouTube workflows in VidStow. The
desktop UI and its narrow request mapping remain VidStow's product boundary.
Engine or provider support does not implicitly widen the desktop application.

A provider is a VidStow capability only when the VidStow repository explicitly
composes it and exposes a corresponding tested workflow. Adding a provider to
the broad engine catalog alone does not change VidStow.

## Repository responsibilities

This repository owns:

- the provider-neutral engine and public compatibility facade;
- the broad CLI and provider catalog;
- provider implementations;
- shared networking, media, output, compatibility, and conformance behavior;
  and
- engine and provider module releases.

The VidStow repository owns:

- the Wails application and UI state;
- focused provider composition;
- desktop request mapping and product policy;
- desktop packaging and release artifacts;
- branding and user-facing desktop documentation; and
- the exact workflows presented as supported by VidStow.

## Current evidence

Dependency and composition tests establish that root `engine` does not depend
on the broad catalog, `pkg/ytdlp.NewClient` owns broad compatibility
composition, and `providers/youtube` is an explicit public composition. VidStow
imports the engine and focused provider rather than the broad catalog.

Runtime routing tests complement package-dependency evidence but do not replace
it.

## Consequences

Provider capability and desktop product capability remain distinct. Go's
package graph exposes composition changes to ordinary review. Shared downloader,
media, and provider behavior remains implemented once rather than copied into a
desktop fork.
