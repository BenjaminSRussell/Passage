# Usability baseline report (template)

Copy this file to `usability-baseline-YYYY-MM-DD.md` in this folder, fill it in after the sessions, and commit it. The first report is the **baseline**: it is taken before the work in #497 lands. Later rounds use the same template and the same tasks (`usability-tasks.md`) so the numbers can be compared.

## Round

| Field | Value |
|---|---|
| Round | baseline, or milestone name |
| Dates | |
| Build tested | git commit and release |
| Environment | demo company and how it was reset between participants |
| Moderators | |
| Participants | count per persona (target: 5), how they were found, years of experience in the job |

## Summary

A paragraph in plain language: what works, what doesn't, and the three things most worth fixing. Then the table below with one row per persona.

| Persona | Participants | Task success (%) | Median time per task | Errors per task | SUS (mean) | Change since baseline |
|---|---|---|---|---|---|---|
| Accounts payable clerk | | | | | | |
| Accounts receivable clerk | | | | | | |
| Buyer | | | | | | |
| Receiving and warehouse | | | | | | |
| Sales representative | | | | | | |
| Production planner | | | | | | |
| Shop floor supervisor | | | | | | |
| Controller | | | | | | |
| People and payroll administrator | | | | | | |
| Administrator | | | | | | |

How the numbers are worked out:

- **Task success** is the share of attempts scored *pass*. Report *pass with help* separately and do not count it as success.
- **Median time** is taken over *pass* attempts only.
- **Errors per task** is the mean number of errors per attempt over all attempts.
- **SUS** is the standard 10-question System Usability Scale, scored 0 to 100: for each odd question take the answer minus 1, for each even question take 5 minus the answer, add them up and multiply by 2.5. Report the mean per persona and the lowest single score.

## Results by persona

Repeat this block for each persona.

### Persona name

| Task | Pass | Pass with help | Fail | Median time (s) | Errors per attempt | Notes |
|---|---|---|---|---|---|---|
| AP1 | | | | | | |

SUS scores per participant: `P1 __, P2 __, P3 __, P4 __, P5 __`.

Top findings for this persona, most serious first. Each one gets an issue under #497 and its number goes here.

| # | What happened | How many of 5 | Severity | Issue |
|---|---|---|---|---|
| 1 | | | blocker, major, minor | |

Severity: *blocker* stops the task for most participants; *major* costs time or needs a hint; *minor* is noticed but doesn't slow people down.

## Findings across personas

Problems seen for more than one persona (the same wording, the same pattern, the same missing feedback). Link each to its issue.

## What participants said

Short quotes, with the persona and the task id, no names.

## Limits of this round

Anything that could have skewed results: participants who knew the product, data problems in the demo company, tasks that were cut short, a build that changed between sessions.

## Sign-off

| Role | Name | Date |
|---|---|---|
| Moderator | | |
| Product owner | | |
