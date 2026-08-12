# Engine E5 hardening evidence

Status: deterministic local regression evidence for the session/publication
boundary added after E4 finite HLS and static DASH resume support.

## Scope and compatibility

E5 changes no public request or scheduler behavior. It preserves the durable
session contract for exactly these finite output paths:

| Path | Durable payload / publication evidence |
| --- | --- |
| Direct HTTP(S), one track | `TestDirectSessionCollisionPreservesTargetAndSupportsPublishOnlyRetry`, `TestDirectSessionsRaceToOneDestinationWithoutReplacement`, and the direct publication fault/recovery tests |
| Direct HTTP(S), mergeable multi-track | `TestMultiTrackSessionCollisionSupportsPublishOnlyRetry`, processing restart tests, and multi-track publication fault/recovery tests |
| Finite HLS VOD | `TestFiniteHLSSessionUsesWorkspacePayloadAndStagedPublication` and signed-URL rotation reuse regression |
| Static DASH | `TestStaticDASHSessionUsesWorkspacePayloadAndStagedPublication` and BaseURL rotation reuse regression |

The session path remains deliberately unavailable for live HLS, dynamic DASH,
YouTube live/post-live, SABR, and UMP. E5 neither adds a scheduler nor changes
VidStow composition or release scope.

The native Windows blocker found on the first manual E5 run had two product
causes: workspace ACL setup did not explicitly bind ownership and its
validator accepted only the incidental ACE layout produced by one Windows
ACL-construction path; session basenames also used host-native volume and path
comparisons. The repaired path constructs a protected SDDL allow-list for the
current user, LocalSystem, and Builtin Administrators, validates exact
allow-only ACEs, and validates portable basenames before mapping them to native
paths. The workflow itself is unchanged; the exact hosted Windows run remains
the coordinator's post-review acceptance check.
## Lease, GC, and publication invariants

- Workspace authority uses a native exclusive lease. A killed holder releases
  that OS lease; `TestCrossProcessLeaseCrashReleasesForBoundedGC` then proves a
  later bounded orphan-collection pass can reclaim the old workspace.
- Deletion/recovery retains its guard and reconciliation rules. A lease, marker,
  quarantine, or unsafe evidence never becomes permission to delete a replaced
  directory.
- Publication is exclusive/no-replace. Two independent sessions racing for one
  basename yield one committed artifact and one collision; no user-owned
  destination is overwritten.
- Pre-replace failures remain publish-only retryable. Post-replace or unknown
  outcomes persist reconciliation evidence; recovery only accepts a matching
  staged/destination fingerprint and journal identity.
- Journals are one complete JSON object. Trailing, concatenated, malformed, or
  oversized evidence is rejected; the journal parser has a bounded fuzz target.

## Native-platform matrix

| Platform | Lease primitive | No-replace publication | Evidence status |
| --- | --- | --- | --- |
| Unix targets supported by `lease_unix.go` | non-blocking `flock` | hard-link create-if-absent, with fingerprint reconciliation | deterministic and cross-process tests exercised on this Unix host |
| Windows | `LockFileEx` | Go `os.Link` create-if-absent path with the same fingerprint/collision checks | test binaries compile here; native Windows runner must execute the bounded test command before release approval |
| Other targets | fail closed (`ErrLeaseUnavailable`) | not an advertised resumable-session platform | intentional |

Run the bounded E5 regression suite with explicit test and fuzz limits:

```sh
go test -timeout 150s ./internal/session ./engine -run 'Test(CrossProcessLeaseCrashReleasesForBoundedGC|HelperProcessLeaseContentionAndRelease|DiscardGuardBlocksCrossProcessRecovery|DirectSession(Collision|PostReplace|Indeterminate|PrePublication)|DirectSessionsRaceToOneDestinationWithoutReplacement|FiniteHLSSession|StaticDASHSession|MultiTrackSession(Collision|PostReplace|Indeterminate|PrePublication|LeaseContention))' -count=1
go test -timeout 60s ./engine -run '^$' -fuzz '^FuzzDirectPublicationJournalEvidence$' -fuzztime=10s
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/ytdlp-session.test.exe ./internal/session
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/ytdlp-engine.test.exe ./engine
```

For native Windows execution, keep the CI job timeout around the equivalent
`go test` selection.
