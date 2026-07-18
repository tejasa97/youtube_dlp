# Phase 3 extractor priority proxy

Status: active until opt-in traffic snapshots provide a measured denominator

The project has no suitable production traffic dataset yet. Wave 2 therefore
uses the fallback ordering policy required by the Phase 3 plan:

1. prefer shared backends that unlock multiple sites or instances;
2. prefer stable anonymous APIs that support deterministic offline fixtures;
3. cover a different protocol or playlist risk class with each increment;
4. avoid routing arbitrary hosts when the product cannot first prove the site;
5. defer authenticated, challenge-heavy, or volatile endpoints to the runtime
   wave where their credentials and transport policies can be tested together.

This proxy selected PeerTube for federated direct/HLS/live behavior,
Internet Archive for public API and reusable multi-file playlists, and
Streamable for a compact direct shared-hosting boundary. It is an engineering
ordering decision, not a claim that these sites represent measured traffic.

Once operators explicitly collect privacy-safe snapshots, the published window
must include errors, unsupported operations, fallbacks, unknown extractors, and
overflow. Wave ordering may then change based on the reproducible denominator;
the parity status of existing extractors remains fixture- and evidence-scoped.
