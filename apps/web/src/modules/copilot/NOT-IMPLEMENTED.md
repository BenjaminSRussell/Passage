# NOT-IMPLEMENTED: copilot module (PSG-47, wave 47)

Slice 2 adds the streaming client: `api/stream.ts` parses `text/event-stream`
frames (`token`, `done`, `error`), validates the terminal `done` frame with the
same schema as the JSON path, and fails loudly when a stream ends before a
terminal frame. The panel calls `askCopilot`, which offers both response types:
it renders tokens as they arrive when the server streams, and reads the JSON
body when it does not.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Server-sent streaming response from `POST /api/v1/copilot/chat` (FR-47.3, first-token p95 < 2 s, 9.47.10) | later slice of PSG-47 | The client side is built and the panel renders streamed tokens. The BFF must send `token`/`done`/`error` frames; that server is `passage-copilot-bff`, outside this module's paths |
| Screens for the agent runtime pieces of slice 2 (kill switch state, token budget status, provider and failover status) | later slice of PSG-47 | `services/agents-ts` exposes no HTTP API yet, so there is nothing to list from. The screens come with the kill-switch, budget and run endpoints (9.47.5, 9.47.6) |
| `POST /api/v1/copilot/chat` server (copilot BFF) | later slice of PSG-47 | The BFF is a separate service (`passage-copilot-bff`), not in this module's paths |
| `Ctrl+/` opens the copilot with the current record context (9.47.6) | later slice of PSG-47 | Needs the shell's keyboard and record context hooks (shell owner, wave 5) |
| Explain-this-record reads the record's audit trail and related documents (FR-47.3) | later slice of PSG-47 | The panel sends `recordRef`; the server side does the reading |
| Mode remembered per workspace per user (9.47.6) | later slice of PSG-47 | Needs the user preference store |
| Draft insertion into a host document with `origin=agent` and run id (BR-47.13) | later slice of PSG-47 | `canInsertDraft` is built; the host documents belong to waves 17 and 30 |
| Summarize workflow history and cases (FR-47.3) | later slice of PSG-47 | `summarize` is a mode with no server handler yet |
| Copilot enablement per legal entity (`copilot.enabled`, 9.47.13) | later slice of PSG-47 | Enabled by the server's response; the module does not check the flag |
| Localized glossary labels from the label framework (9.47.20) | later slice of PSG-47 | Labels are English constants in `domain.ts` |
| Keyboard approval shortcuts `A`/`R` and agent inbox (9.47.6) | handoff | Belongs to the workflow inbox (wave 4), not this module |
| Agent runs list and timeline, agent administration (mode, thresholds, kill switch), eval dashboard and token budget screens (9.47.6, 9.47.14) | later slice of PSG-47 | services/agents-ts has no HTTP API yet (`api/openapi.yaml` has no paths), so there is nothing to list from; the screens come with the runs API |
| `/api/v1/mcp` route in the web BFF to the gateway's `POST /mcp` | handoff | The BFF belongs to the shell owner (wave 5); the gateway HTTP server is a later slice of PSG-47 (services/mcp-ts/NOT-IMPLEMENTED.md) |
