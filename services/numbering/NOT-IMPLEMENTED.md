# Not implemented in PSG-5 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 3: non-continuous block preallocation and continuous return-to-pool | partial | All three modes persist and allocate under a row lock (gap-free semantics). AX-style segment caching and unused-number return are slice 2 |
| Shared sequences across legal entities, fiscal-calendar segments, user/manual number override | slice 2 | line budget; per-LE scoped sequences cover the proof |
| Argo CD Application, ingress, image registry entry, Kubernetes Secret `numbering-db`, database `passage_numbering` | handoff | `deploy/` outside the service chart is wave 1 |
