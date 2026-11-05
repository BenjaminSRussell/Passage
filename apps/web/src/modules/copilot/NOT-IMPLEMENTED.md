# NOT-IMPLEMENTED: copilot module (PSG-47, wave 47)

Slice 2 adds the streaming client: `api/stream.ts` parses `text/event-stream`
frames (`token`, `done`, `error`), validates the terminal `done` frame with the
same schema as the JSON path, and fails loudly when a stream ends before a
terminal frame. The panel calls `askCopilot`, which offers both response types:
it renders tokens as they arrive when the server streams, and reads the JSON
body when it does not. Slice 5 adds the agent kill switch page
(`KillSwitchPage.tsx`, `/copilot/kill-switch`, nav gated by agents.admin) on the
agent runtime's new `GET`/`POST /api/v1/agents/kill-switch`.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Server-sent streaming response from `POST /api/v1/copilot/chat` (FR-47.3, first-token p95 < 2 s, 9.47.10) | later slice of PSG-47 | The client side is built and the panel renders streamed tokens. The BFF must send `token`/`done`/`error` frames; that server is `passage-copilot-bff`, outside this module's paths |
| Screens for the rest of the agent runtime: token budget status, provider and failover status (9.47.6) | later slice of PSG-47 | The kill switch screen is built. `services/agents-ts` serves no budget or provider endpoint yet (9.47.5 `GET /api/v1/agents/budgets/{tenant}` comes with the persisted budgets) |
| `POST /api/v1/copilot/chat` server (copilot BFF) | later slice of PSG-47 | The BFF is a separate service (`passage-copilot-bff`), not in this module's paths |
| `Ctrl+/` opens the copilot with the current record context (9.47.6) | later slice of PSG-47 | Needs the shell's keyboard and record context hooks (shell owner, wave 5) |
| Explain-this-record reads the record's audit trail and related documents (FR-47.3) | later slice of PSG-47 | The panel sends `recordRef`; the server side does the reading |
| Mode remembered per workspace per user: the panel saves and restores the mode through an injected `memory` store (`domain.ts`, `CopilotPanel`) | handoff | The shell (wave 5) must pass the browser store and the signed-in user id and workspace. Without it the mode is not remembered |
| Draft insertion into a host document with `origin=agent` and run id (BR-47.13) | later slice of PSG-47 | `canInsertDraft` is built; the host documents belong to waves 17 and 30 |
| Summarize workflow history and cases (FR-47.3) | later slice of PSG-47 | `summarize` is a mode with no server handler yet |
| Copilot enablement per legal entity (`copilot.enabled`, 9.47.13) | later slice of PSG-47 | Enabled by the server's response; the module does not check the flag |
| Localized glossary labels from the label framework (9.47.20) | later slice of PSG-47 | Labels are English constants in `domain.ts` |
| Approval decision screen, keyboard approval shortcuts `A`/`R` and agent inbox (FR-47.8, 9.47.6) | handoff | The decision endpoint is built in services/agents-ts (`POST /api/v1/agents/runs/{run_id}/actions/{id}/decision`). The screen that shows the proposal and its diff and calls it belongs to the workflow inbox (wave 4), which receives `approval.requested` |
| Agent runs list and timeline, agent administration (mode, thresholds), eval dashboard and token budget screens (9.47.6, 9.47.14) | later slice of PSG-47 | services/agents-ts serves only the kill switch and the approval decision so far; the runs, budgets and versions endpoints are not built, so there is nothing to list from. 9.47.5 names no read endpoint for agent definitions or versions, so the agent administration screen also needs one added to the API |
| `/api/v1/mcp` route in the web BFF to the gateway's `POST /mcp` | handoff | The BFF belongs to the shell owner (wave 5); the gateway HTTP server is a later slice of PSG-47 (services/mcp-ts/NOT-IMPLEMENTED.md) |
