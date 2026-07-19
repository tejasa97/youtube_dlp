## Outcome

Describe the user-visible result and the problem it solves.

## Compatibility and deviations

State the claimed corpus, manifest changes, known deviations, and whether any
public API or CLI behavior changes.

## Security and privacy

Describe trust-boundary, credential, path, process, network, and fixture-data
effects. Write `None` only after reviewing them.

## Evidence

List deterministic fixtures, provenance records, success/failure/cancellation
tests, and relevant fuzz or platform evidence.

## Local verification

GitHub Actions is temporarily disabled. Check every command you ran:

- [ ] `test -z "$(gofmt -l .)"`
- [ ] `go mod tidy -diff`
- [ ] `go vet ./...`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go run ./cmd/paritycheck`
- [ ] Relevant fuzz, cross-platform, media, or Python-free Docker checks

## Submission review

- [ ] The change is scoped and contains no unrelated edits.
- [ ] Documentation and compatibility claims match automated evidence.
- [ ] Fixtures follow `docs/FIXTURE_POLICY.md` and contain no secrets or private data.
- [ ] New third-party material has compatible licensing and retained notices.
- [ ] I have the right to submit this work under the project's contribution terms.
