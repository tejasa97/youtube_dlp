# Operations control deviations

- `ExecuteControlled` enforces expiry and rate policy before invocation, but
  the deployment owner must serialize concurrent authorization and atomically
  persist the returned ledger. This package deliberately has no filesystem,
  database, scheduler, credential resolver, or regional transport.
- A `Runner` that ignores cancellation may retain its goroutine. Untrusted
  runners require deployment-owned process isolation.
- Replay captures reproduce bounded semantic outcomes only. They do not store
  raw traffic, secrets, URLs, regions, errors, timestamps, or media, so they
  cannot reproduce remote-server or transport behavior.
- The conformance policy and replay are synthetic offline evidence. They do
  not represent public, authenticated, or regional production canary runs.
