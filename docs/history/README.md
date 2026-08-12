# Historical engineering records

These documents record evidence at historical baselines. They are not current
user documentation or compatibility claims.

Use [`CURRENT_SCOPE.md`](../CURRENT_SCOPE.md) and
[`PROJECT_STATUS.md`](../PROJECT_STATUS.md) for the current boundary.

## Port program

| Record | Purpose |
| --- | --- |
| [Go port evaluation](port-program/GO_PORT_EVALUATION.md) | Original feasibility and risk assessment |
| [Zero-Python program plan](port-program/ZERO_PYTHON_GO_PORT_PLAN.md) | Long-range native-port program definition |
| [Phase 0 plan](port-program/PHASE_0_IMPLEMENTATION_PLAN.md) | Foundation and walking skeleton |
| [Phase 1 plan](port-program/PHASE_1_IMPLEMENTATION_PLAN.md) | Risk-retirement pilot |
| [Phase 2 plan](port-program/PHASE_2_IMPLEMENTATION_PLAN.md) | Native foundation and alpha |
| [Phase 3 plan](port-program/PHASE_3_IMPLEMENTATION_PLAN.md) | High-value coverage and beta preparation |

## Exit reviews

- [Phase 0 exit review](../PHASE_0_EXIT_REVIEW.md)
- [Phase 1 exit review](../PHASE_1_EXIT_REVIEW.md)
- [Phase 1 differential review](../P1_DIFFERENTIAL_REVIEW.md)
- [Phase 2 exit review](../PHASE_2_EXIT_REVIEW.md)
- [Phase 2 security review](../P2_SECURITY_REVIEW.md)
- [Phase 3 exit review](../PHASE_3_EXIT_REVIEW.md)

## Independent audits

- [Coverage and parity audit](../audits/P3_COVERAGE_AND_PARITY_AUDIT.md)
- [Security, privacy, and isolation audit](../audits/P3_SECURITY_PRIVACY_ISOLATION_AUDIT.md)
- [Release, operations, and ABI audit](../audits/P3_RELEASE_OPERATIONS_ABI_AUDIT.md)

## Reading historical records

- Treat dates, package names, branch names, and maturity labels as descriptions
  of the recorded baseline.
- Do not rewrite an independent audit to match later remediation. Link the
  remediation from a newer status or exit review instead.
- Do not infer current feature support from a phase plan. Verify the capability
  manifest, supported-site documentation, and current tests.
- Historical `docs/P1_*.md`, `docs/P2_*.md`, and `docs/P3_*.md` lane reports
  remain at their original paths to preserve evidence links.
