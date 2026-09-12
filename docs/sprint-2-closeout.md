# Ember Sprint 2 closeout receipt

This receipt reconciles the Milestone 10 closeout against the code merged on
`main`. It is repository evidence for the local-first product; it is not an
authorization to publish, deploy, release, change repository visibility, or
enable the attached GitHub Wiki.

## Closeout status

The following closeout issues have complete merged implementation or evidence
and were verified together from a fresh checkout:

| Issue | Area | Merged evidence |
|---|---|---|
| #174 | Partial failure, rollback, and recovery visibility | PR #184 |
| #175 | Secrets, redaction, non-listability, and audit | PR #185 |
| #176 | Observability and operation visibility | PR #186 |
| #177 | Portal and CLI operator completion | PR #187 |
| #178 | Upgrade, rollback, and release evidence | PR #188 |
| #179 | Resource lifecycle, locks, safe deletion, and Blob completeness | PR #189 |
| #180 | Queue and event completion evidence | PR #190 |
| #181 | Azure-shaped compatibility surface | PR #191 |
| #182 | Source-grounded documentation and learning surface | PR #192 |

This closeout receipt completes the final reconciliation for #183. The
closeout PR closes #174 and #183 only after the evidence below has passed; it
does not close older roadmap parents or unrelated follow-up issues.

## Verification matrix

The canonical `scripts/ember-release-evidence.sh` gate passed from the clean
checkout and covered:

- full Go tests;
- race tests;
- `go vet`;
- trimmed CLI build;
- platform-labelled package creation;
- SHA-256 package provenance verification; and
- the disposable clean-machine lifecycle smoke path.

The recovery-specific acceptance path also passed:

- bounded partial-apply progress persisted and reopened;
- CLI and HTTP inspection exposed the failure boundary;
- completed creates were rolled back;
- repeating the same recovery request replayed the durable result without a
  second mutation; and
- reset removed the recovery records from clean state.

The documentation link and command-reference checker is part of the closeout
verification and must pass with this receipt present.

## Remaining boundaries and follow-ups

No Milestone 10 feature acceptance criterion remains open after this receipt is
merged. The following boundaries remain explicit and are not silently changed:

- The attached GitHub Wiki remains gated by issue #149; no Wiki enablement or
  Wiki content mutation is performed here.
- Public visibility, deployment, publication, and release actions remain
  outside this repository change.
- Older roadmap parents and follow-ups, including #23, #30, #36, #38, #39,
  #40, #42, #141, and #143–#149, remain separately tracked unless their own
  acceptance and approval gates are completed.
- Ember remains a bounded, local-first, single-node implementation and makes no
  claim of hosted Azure wire compatibility, multi-node behavior, or cloud
  deployment.
