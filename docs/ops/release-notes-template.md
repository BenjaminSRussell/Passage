# Passage vX.Y.Z

<!--
Release notes template (PRD 9.50.15: version, digest, features by module, migrations, breaking changes,
CVE fixes; published as HTML, Markdown and PDF). Copy this file, replace every placeholder and run
`python3 docs/ops/tools/check_release_notes.py <notes>.md --compat-evidence compat-evidence-<version>.json`.
The gate ignores these comments and fails while a placeholder (`<...>`, `vX.Y.Z`, `YYYY-MM-DD`, `NNNN`) is left.
-->

Release date: YYYY-MM-DD. Commit: `<git sha>`. Previous release: `vX.Y.Z`.

## Images and charts

<!-- Publish by digest only (FR-50.12). One line per image or chart. -->

- `passage-<service>` `sha256:<64 lowercase hex characters>` (chart `passage-<service>` version `X.Y.Z`)

SBOM (SPDX 2.3 JSON) and cosign signature references: `<registry>/<image>@sha256:<digest>`.

## Features by module

<!-- One sub-heading per module. Write only what a customer can see or operate. -->

### <module>

- <feature, with its PRD requirement id, for example FR-NN.N>

## Migrations

<!--
List every migration file in the release, in order, with its phase (expand, backfill, switch or contract).
Contract steps are deferred to the next release (BR-50.7, FR-50.7). Write `None` if there are no migrations.
-->

- `NNNN_<name>.sql` (expand)

## Breaking changes

<!--
None are allowed for N-1 clients (PRD 9.50.15, AC-50.5), so this section is `None` and the gate requires it.
The N/N-1 evidence is `deploy/multi-region/tools/compat_evidence.py`; pass its file with --compat-evidence and
the gate fails when the evidence reports a breaking service. Only a release that deliberately breaks N-1 (a
new major version, decided by the release manager) is checked with --allow-breaking and lists each change
with its replacement and the release in which the old form is removed.
-->

None

## CVE fixes

<!--
List each fixed vulnerability by identifier, package and fixed version. Write `None` if there are none.
Release criteria: 0 critical, and 0 high older than 30 days (PRD 9.50.1).
-->

None

## Rollback

<!-- State the previous version to roll back to, and whether the rollback needs a contract step reverted. -->

Roll back to `vX.Y.Z` by digest; <contract step to revert, or "no contract step in this release">.
