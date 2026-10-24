# Remaining Wave 03 handoffs

| Deliverable or proof | Status | Reason |
|---|---|---|
| Atlas or Goose versioned migrations | deferred to Wave 01 | The repository standard remains plain SQL through `dbx.Migrate`; Wave 03 does not own pipeline tooling. |
| Tenant, LegalEntity and User runtime replacement | blocked on Wave 02 | The generated fixtures and runtime artifacts exist, but PRs #56/#57 are not merged and identity still owns those persistence contracts. |
| Named-user classification source | blocked on Wave 02 | `internal/usage` accepts an identity `Source`, aggregates tenant/legal-entity/role usage and exports CSV; the authoritative named-user/principal feed is not on `main`. |
| Nightly usage scheduling | blocked on Wave 04 | The collector is callable and deterministic, but durable scheduling belongs to the batch/workflow service. |
| Generated entity-changed event | blocked on Wave 04 | The registry migration and service are present; Wave 04 owns the event backbone and schema registry. |
| Lifecycle hook execution | deferred | Hooks and customer hook overlays are parsed, versioned and generated. A safe expression runtime is not yet selected, so arbitrary hook expressions are not executed. |
| Audit-field persistence | kernel handoff | Audit metadata is emitted in JSON Schema, but the shared audit-event/outbox runtime is Wave 0/5 owned. |
| CI image and metadata-specific chart lines | Wave 01 handoff | `.github/workflows/` is outside Wave 03 ownership. Root Go tests already discover the module; local chart lint covers the chart. |
| Live stage / three-pod cluster proof | environment handoff | The Helm chart requests three replicas and the load contract starts three independent API nodes. A live Argo stage cluster is not available in this branch. |
