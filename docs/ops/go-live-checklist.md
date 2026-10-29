# Production cutover go/no-go checklist (9.50.1)

Executed at T+24 h of the production cutover. Any open item is NO-GO. The decision is recorded via
`cutover.decision.recorded` with the evidence links and approvers (CutoverDecisionOwner).

| # | Item | Evidence | Owner | Status |
|---|---|---|---|---|
| 1 | All wave-49 packs RECONCILED, 0 MISMATCH | migration reconciliation report | Migration lead | open |
| 2 | Parallel run for the prior month SIGNED_OFF, 0 unexplained | parallel-run sign-off | Customer controller | open |
| 3 | Opening trial balance signed by the customer controller | signed TB | Customer controller | open |
| 4 | SoD evaluation: 0 unmitigated HIGH conflicts for go-live users | SoD report | Security lead | open |
| 5 | Country packs installed; one statutory fixture per entity executed | fixture run log | Localization lead | open |
| 6 | Load test on the production cluster at 50% of target within SLOs | k6 campaign certificate | Performance lead | open |
| 7 | DR game day passed within 30 days before cutover | game-day report | SRE on-call | open |
| 8 | Backup verified: PITR drill on production data within 7 days | drill record | DBA | open |
| 9 | Security: pen-test high/critical = 0, secrets rotated, images signed | pen-test report, cosign verify | Security lead | open |
| 10 | Bank, EDI, e-invoicing, carrier endpoints smoke-tested in production | smoke log | Integration lead | open |
| 11 | User acceptance sign-off for 12 core processes | UAT sign-off | Customer process owners | open |
| 12 | Support rota, escalation and rollback decision owner named | rota document | Support lead | open |
| 13 | Monitoring dashboards and alerts green for 24 h | dashboard export | SRE on-call | open |
| 14 | Legacy freeze confirmed and rollback plan rehearsed | freeze notice, rehearsal record | Project manager | open |

Status values: open, done, waived (waiver requires CutoverDecisionOwner signature; not allowed for items 1-4 or 8-9).

## Hypercare (T+1 d to T+30 d)
24x5 on-call; Sev-1 response 15 min, Sev-2 1 h. Daily 09:00 customer stand-up in the tenant time zone.
Exit: first month-end reconciled; Sev-1 count 0 for 14 consecutive days; Sev-2 backlog <= 5; SLOs met for
30 days; customer sign-off.
