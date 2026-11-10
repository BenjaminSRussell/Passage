# Usability gate

The bar a release has to meet for the flows it changes. It stops a release from making common tasks harder without anyone noticing. The release checklist (`docs/ops/release-notes-template.md`, "Usability gate") points here.

## Which flows are in scope

A flow is in scope for a release when the release changes a screen, route, label, message or default on the path of any task in `usability-tasks.md`. The release manager lists the in-scope task ids in the release notes. A release that changes none says so and skips the rest of this page.

## The bar

Measured against the latest baseline or milestone report (`usability-baseline-report.md`) for the same persona and the same task ids:

| Measure | The release passes when |
|---|---|
| Task success | Not lower than the reference by more than 20 percentage points (one participant of five) for any in-scope task. |
| Time on task | The median time for an in-scope task is not more than 25% above the reference. |
| Errors | The mean errors per attempt for an in-scope task do not rise by more than 0.5. |
| SUS | The persona's mean SUS is not more than 5 points below the reference, and not below 68 (the usual "average" mark) where the reference was above 68. |
| Blockers | No open *blocker* finding on an in-scope flow. |

With five participants one person is 20 points of success, so the success rule allows one more failure than the reference and no more (a drop of more than 20 points fails). A result that fails one measure is not waved through by passing the others.

## How it is checked

1. **Every release.** The release manager lists the in-scope task ids and checks the open findings (issues under #497 labelled for the persona) for blockers. No sessions are needed if the changed flows are covered by the previous round and nothing on their path changed since.
2. **After each major #497 milestone.** A new round with 5 participants per affected persona, the in-scope tasks only, written up with the template. The report is committed and linked from the release notes.
3. **Telemetry as a second check.** When the funnel events from #510 are live, the release manager compares each in-scope funnel (drop-off per step, validation errors per field) with the previous release. A step whose drop-off is 5 points worse needs an explanation in the notes. Telemetry never replaces a session round for a milestone.

## Deciding

| Outcome | What happens |
|---|---|
| Pass | The release proceeds. The release notes link the report or state that no in-scope flow changed. |
| Fail | The release manager either fixes the flow and re-measures the failed tasks, or holds the release. |
| Waived | Only the product owner can waive, in writing in the release notes, naming the measure, the cause and the issue that will fix it. A waiver covers one release. A blocker finding can't be waived. |

## Release checklist entry

The release notes carry a "Usability gate" section with one of:

- `Not in scope: no screen on a task path changed.`
- `Passed: tasks <ids>, report <link>.`
- `Waived: <measure>, <cause>, <issue>, approved by <name>.`
