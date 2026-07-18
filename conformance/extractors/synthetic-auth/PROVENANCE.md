# Synthetic authenticated fixture provenance

These fixtures are project-owned deterministic conformance data. They are not
derived from a third-party service or copied from the pinned yt-dlp checkout.
The reserved RFC 2606 `.invalid` origins ensure production binaries cannot
accidentally contact a real service.

The flow models the behavior that must remain common to authenticated
extractors:

1. browser/imported cookies are loaded into the per-operation transport jar;
2. the extractor issues a request to the protected origin without reading or
   copying cookie values;
3. the shared transport attaches only cookies applicable to that origin/path;
4. HTTP 401/403 and a response without an authenticated session are
   authentication failures;
5. successful metadata is normalized without retaining authentication values.

The tests use the repository's real `internal/network.Client` with an injected
offline `RoundTripper`, not a jar-aware extractor fake. This proves the same jar
boundary used by browser-cookie imports while keeping the test deterministic
and network-free.
