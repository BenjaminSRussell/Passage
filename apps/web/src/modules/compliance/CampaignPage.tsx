import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, Textarea, type DataTableColumn } from "../../shell/ui";
import { closeCampaign, describeProblem, planCampaign, type CampaignCloseResult, type CampaignEntry, type CertDecision, type Problem } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";

const DECISIONS: CertDecision[] = ["CERTIFY", "REVOKE", "PENDING"];

/**
 * Reads "manager, user, role[, decision]" lines. An empty decision is
 * PENDING; the line number of the first bad line is reported.
 */
export function parseEntries(text: string): { entries: CampaignEntry[]; error?: string } {
  const entries: CampaignEntry[] = [];
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]?.trim() ?? "";
    if (!line) continue;
    const [managerId = "", userId = "", roleName = "", decision = ""] = line.split(",").map((p) => p.trim());
    if (!managerId || !userId || !roleName) return { entries: [], error: `Line ${i + 1}: give a manager, a user and a role.` };
    const d = decision.toUpperCase();
    if (d && !DECISIONS.includes(d as CertDecision)) return { entries: [], error: `Line ${i + 1}: the decision must be certify, revoke or pending.` };
    entries.push({ managerId, userId, roleName, ...(d ? { decision: d as CertDecision } : {}) });
  }
  if (entries.length === 0) return { entries, error: "Enter at least one line." };
  return { entries };
}

const columns: DataTableColumn<CampaignEntry>[] = [
  { key: "managerId", header: "Manager", mono: true },
  { key: "userId", header: "User", mono: true },
  { key: "roleName", header: "Role" },
];

/**
 * Certification campaign (PRD §9.48.6, duty SoDAdmin): plan the window of an
 * access certification campaign, then check its progress and what closing it
 * does (FR-48.6, BR-48.9). Nothing is stored yet: the manager decisions are
 * entered here until campaigns are recorded.
 */
export function CampaignPage() {
  const [start, setStart] = useState("");
  const [windowDays, setWindowDays] = useState("");
  const [planErrors, setPlanErrors] = useState<{ start?: string; windowDays?: string }>({});
  const [closesAt, setClosesAt] = useState<string | null>(null);
  const [lines, setLines] = useState("");
  const [autoRevoke, setAutoRevoke] = useState(false);
  const [linesError, setLinesError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<CampaignCloseResult | null>(null);
  const [busy, setBusy] = useState<"plan" | "close" | null>(null);

  async function plan(event: FormEvent) {
    event.preventDefault();
    const errors: { start?: string; windowDays?: string } = {};
    if (!start) errors.start = "Enter the first day of the campaign.";
    let days: number | undefined;
    if (windowDays.trim()) {
      days = Number(windowDays.trim());
      if (!Number.isInteger(days) || days < 3 || days > 60) errors.windowDays = "Enter whole days from 3 to 60.";
    }
    setPlanErrors(errors);
    if (Object.keys(errors).length > 0) return;
    setBusy("plan");
    setFailure(null);
    try {
      const out = await planCampaign({ startsAt: new Date(`${start}T00:00:00Z`).toISOString(), ...(days !== undefined ? { windowDays: days } : {}) });
      setClosesAt(out.closesAt);
      setResult(null);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function check(event: FormEvent) {
    event.preventDefault();
    const { entries, error } = parseEntries(lines);
    setLinesError(error);
    if (error || !closesAt) return;
    setBusy("close");
    setFailure(null);
    try {
      setResult(await closeCampaign({ entries, autoRevoke, closesAt }));
    } catch (e) {
      setFailure(describeProblem(e));
      setResult(null);
    } finally {
      setBusy(null);
    }
  }

  const overdue = result ? Object.values(result.progress.overdueByManager).reduce((a, b) => a + b, 0) : 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Certification campaign"
        description="Managers certify or revoke each direct report's roles within the campaign window. At the close, revoked roles are removed; roles nobody decided on are removed only when auto-revoke is on and flagged otherwise."
      />
      <Card title="Window">
        <form className={styles.form} onSubmit={plan} noValidate aria-label="Plan campaign">
          <FormField id="cmp-start" label="Starts on (UTC)" error={planErrors.start}>
            {(control) => <Input {...control} type="date" value={start} onChange={(e) => setStart(e.target.value)} invalid={Boolean(planErrors.start)} />}
          </FormField>
          <FormField id="cmp-days" label="Window (days)" hint="14 when left empty." error={planErrors.windowDays} optional>
            {(control) => <Input {...control} inputMode="numeric" value={windowDays} onChange={(e) => setWindowDays(e.target.value)} invalid={Boolean(planErrors.windowDays)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "plan"}>
              Plan window
            </Button>
          </div>
        </form>
        <Alert tone="info" live="status">
          {closesAt ? `The campaign closes ${formatDateTime(closesAt)}.` : null}
        </Alert>
      </Card>
      {closesAt ? (
        <Card title="Decisions">
          <form className={styles.form} onSubmit={check} noValidate aria-label="Check campaign close">
            <div className={styles.wide}>
              <FormField id="cmp-lines" label="Lines" hint="One per line: manager, user, role, decision (certify, revoke, or empty for not yet decided)." error={linesError}>
                {(control) => <Textarea {...control} rows={6} className={styles.mono} value={lines} onChange={(e) => setLines(e.target.value)} invalid={Boolean(linesError)} />}
              </FormField>
            </div>
            <span className={`${styles.check} ${styles.wide}`}>
              <Input id="cmp-auto" type="checkbox" checked={autoRevoke} onChange={(e) => setAutoRevoke(e.target.checked)} />
              <label htmlFor="cmp-auto">Auto-revoke roles nobody decided on at the close</label>
            </span>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "close"}>
                Check close
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Alert tone="danger" title={failure ? "The campaign check was refused" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      {result ? (
        <>
          <Card title={result.closed ? "At the close" : `Progress as of ${formatDateTime(result.asOf)}`}>
            <StatGroup label="Campaign progress">
              <StatTile label="Certified" value={`${result.progress.percentCertified}%`} hint={`${result.progress.certified} of ${result.progress.total}`} />
              <StatTile label="Revoked" value={result.progress.revoked} />
              <StatTile label="Not decided" value={result.progress.pending} tone="warning" />
              <StatTile label="Overdue" value={overdue} tone="danger" hint={result.closed ? "Roles their manager did not decide on" : "The window is still open"} />
            </StatGroup>
            {Object.keys(result.progress.overdueByManager).length > 0 ? (
              <ul className={styles.badges} aria-label="Overdue by manager">
                {Object.entries(result.progress.overdueByManager).map(([m, n]) => (
                  <li key={m}>
                    <Badge tone="danger">{`${m}: ${n}`}</Badge>
                  </li>
                ))}
              </ul>
            ) : null}
          </Card>
          <DataTable
            label="Roles to revoke"
            columns={columns}
            rows={result.revoke}
            getRowKey={(e) => `${e.userId}|${e.roleName}`}
            empty={{ title: "Nothing to revoke" }}
          />
          {result.closed ? (
            <DataTable label="Flagged roles" columns={columns} rows={result.flagged} getRowKey={(e) => `${e.userId}|${e.roleName}`} empty={{ title: "Nothing flagged", description: "Every role was decided on, or auto-revoke removed it." }} />
          ) : null}
        </>
      ) : (
        <Card>
          <EmptyState icon="info" title="No campaign checked" description="Plan the window, then enter the managers' decisions to see the progress and the close." compact />
        </Card>
      )}
    </div>
  );
}
