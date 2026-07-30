# Residual shared-backend adapter audit

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

This bounded audit reviewed five residual classes associated heuristically or
directly with the existing Cloudflare Stream, ThePlatform, or Anvato backends.
None satisfies the complete adapter-only and end-to-end credential-isolation
gate without changing a shared backend or adding forbidden infrastructure.
All five therefore remain deferred, with their checklist rows unchanged.

## Candidate decisions

| Upstream class | Decision | Reason |
| --- | --- | --- |
| `ClipchampIE` | Deferred | The pinned class adds `parentOrigin=https://clipchamp.com` to Cloudflare manifest requests. The existing Go Cloudflare backend accepts the delivery ID/JWT but derives fresh canonical manifest URLs and does not preserve that adapter-specific query. Exact support would require changing backend semantics. |
| `CorusIE` | Deferred | The pinned class downloads Corus playlist JSON and parses each source-provided SMIL document, including self-hosted sources. A ThePlatform or ThePlatformFeed child cannot represent the complete route corpus without duplicating generic SMIL parsing. |
| `NBCNewsIE` | Deferred | The pinned `videoAssets` loop combines direct MP4 assets, direct HLS manifests, and optional ThePlatform URLs. Emitting only a ThePlatform child would be a forbidden partial implementation. |
| `ScrippsNetworksWatchIE` | Deferred | The Anvato MCP ID is available only after Cognito OpenID token issuance, STS `AssumeRoleWithWebIdentity`, and AWS SigV4 execution. Those authentication and signing systems are outside this adapter-only task. |
| `ScrippsNetworksIE` | Deferred | Its URL-to-ThePlatform account/GUID mapping is deterministic and needs no adapter request, but product re-entry invokes the unchanged ThePlatform backend. That backend performs SMIL and preview discovery through ordinary `Transport.Do`; it does not prove stripping backend-applicable ambient/global Authorization, Proxy-Authorization, Referer, or cookies. Satisfying the mandatory product credential-isolation gate would require a shared-backend semantic change forbidden here. |

## Scope outcome

- No extractor key was added or registered.
- No exact alias or checklist promotion was added.
- No success fixture or supported-site claim was added.
- No downloader, manifest parser, authentication system, redirect policy,
  credential policy, or shared-backend normalization was changed.
- `cloudflarestream.go`, `theplatform.go`, and `shared_hosting.go` remain
  byte-for-byte unchanged from the requested base.

The focused regression
`internal/upstreamdelta.TestResidualAdapterExactAliasesRemainConservative`
asserts that none of the five audited classes can acquire a curated exact alias
without an explicit future review.
