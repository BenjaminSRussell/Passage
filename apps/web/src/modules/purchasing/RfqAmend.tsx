import { useMemo, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import { acknowledgeRFQAmendment, amendRFQ, describeProblem, editRFQ, newIdempotencyKey, type RankingRule, type RFQ, type RFQAmendment } from "./api/client";
import { CriteriaEditor, criteriaInput, newCriterion, validateCriteria, type CriterionDraft } from "./RfqTypePages";
import { formatInstant, rankingLabels, type Failure } from "./parts";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

/** An instant as a datetime-local value in the user's local time. */
export function toLocalInput(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** The number of an addendum as vendors see it: RFQ-00000007-A1. */
export const amendmentRef = (rfq: RFQ, a: RFQAmendment) => `${rfq.number}-A${a.amendmentNo}`;

/** Mirrors the service rules on an amendment so most mistakes are caught before the round trip. */
export function validateAmendment(
  rfq: RFQ,
  f: { summary: string; deadline: string; replaceCriteria: boolean; criteria: CriterionDraft[] },
  now = new Date(),
): Errors {
  const e: Errors = f.replaceCriteria ? validateCriteria(rfq.rankingRule, f.criteria) : {};
  if (f.summary.trim() === "") e.summary = "Say what changed.";
  else if (f.summary.trim().length > 2000) e.summary = "Use at most 2000 characters.";
  if (f.deadline !== "") {
    const d = new Date(f.deadline);
    if (Number.isNaN(d.getTime())) e.replyDeadline = "Enter a valid deadline.";
    else if (d <= now || d <= new Date(rfq.replyDeadline)) e.replyDeadline = "Choose a deadline after now and after the current deadline.";
  } else if (now >= new Date(rfq.replyDeadline)) {
    e.replyDeadline = "The deadline has passed; give a later one.";
  }
  return e;
}

/** Edit of a DRAFT RFQ: deadline, ranking rule, sealed flag and scoring criteria (FR-17.19). */
export function EditDraftForm({ rfq, onSaved }: { rfq: RFQ; onSaved(rfq: RFQ): void }) {
  const [deadline, setDeadline] = useState(() => toLocalInput(rfq.replyDeadline));
  const [rule, setRule] = useState<RankingRule>(rfq.rankingRule);
  const [sealed, setSealed] = useState(rfq.sealed ?? false);
  const [criteria, setCriteria] = useState<CriterionDraft[]>(() => (rfq.criteria ?? []).map((c) => newCriterion(c.name, c.weight)));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateCriteria(rule, criteria);
    const d = new Date(deadline);
    if (deadline === "" || Number.isNaN(d.getTime())) found.replyDeadline = "Enter the reply deadline.";
    else if (d <= new Date()) found.replyDeadline = "Choose a deadline in the future.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await editRFQ(rfq.id, rfq.version, {
        replyDeadline: d.toISOString(),
        rankingRule: rule,
        sealed,
        criteria: criteriaInput(criteria),
      });
      onSaved(body);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={onSubmit} noValidate aria-label="Edit draft">
      <Card title="Edit draft">
        <div className={styles.grid}>
          <FormField id="rfq-edit-deadline" label="Reply deadline" hint="Your local time" error={errors.replyDeadline}>
            {(c) => <Input {...c} type="datetime-local" value={deadline} onChange={(e) => setDeadline(e.target.value)} />}
          </FormField>
          <FormField id="rfq-edit-ranking" label="Rank replies by">
            {(c) => (
              <Select {...c} value={rule} onChange={(e) => setRule(e.target.value as RankingRule)}>
                {(Object.keys(rankingLabels) as RankingRule[]).map((k) => (
                  <option key={k} value={k}>
                    {rankingLabels[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.checkboxRow}>
          <Input id="rfq-edit-sealed" type="checkbox" checked={sealed} disabled={rfq.sealedRequired} onChange={(e) => setSealed(e.target.checked)} />
          <label htmlFor="rfq-edit-sealed">
            Sealed bids: keep replies hidden until the deadline and refuse late replies
            {rfq.sealedRequired ? " (required by the RFQ type)" : ""}
          </label>
        </div>
      </Card>
      <CriteriaEditor id="rfq-edit" criteria={criteria} onChange={setCriteria} errors={errors} />
      <Alert tone="danger" live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <div className={styles.actions}>
        <Button type="submit" variant="secondary" loading={busy}>
          Save draft changes
        </Button>
      </div>
    </form>
  );
}

/**
 * Why the scoring criteria can no longer be replaced, or null while they can. Mirrors the service: not once a
 * vendor is scored, and not once an RFQ that is not sealed has replies the buyer can read.
 */
export function criteriaLock(rfq: RFQ): string | null {
  if ((rfq.scores ?? []).length > 0) return "vendors are already scored";
  if (!rfq.sealed && Math.max(rfq.replyCount ?? 0, (rfq.replies ?? []).length) > 0) return "replies are already open to you";
  return null;
}

/** Amendments of a sent RFQ: the issued addenda with vendor acknowledgements, and the form for a new one (FR-17.19). */
export function AmendmentsPanel({ rfq, onChanged }: { rfq: RFQ; onChanged(rfq: RFQ): void }) {
  const amendments = rfq.amendments ?? [];
  const open = rfq.status === "SENT" || rfq.status === "REPLIES_OPEN";
  const allowed = rfq.amendmentsAllowed !== false;
  const locked = criteriaLock(rfq);
  const [summary, setSummary] = useState("");
  const [deadline, setDeadline] = useState("");
  const [replace, setReplace] = useState(false);
  const [criteria, setCriteria] = useState<CriterionDraft[]>(() => (rfq.criteria ?? []).map((c) => newCriterion(c.name, c.weight)));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const key = useMemo(() => newIdempotencyKey(), [rfq.version, summary, deadline, replace, criteria]);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateAmendment(rfq, { summary, deadline, replaceCriteria: replace, criteria });
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy("amend");
    try {
      const { body } = await amendRFQ(
        rfq.id,
        rfq.version,
        {
          summary: summary.trim(),
          replyDeadline: deadline === "" ? undefined : new Date(deadline).toISOString(),
          criteria: replace ? criteriaInput(criteria) : undefined,
        },
        key,
      );
      onChanged(body);
      setSummary("");
      setDeadline("");
      setReplace(false);
      setCriteria((body.criteria ?? []).map((c) => newCriterion(c.name, c.weight)));
      setNotice(`Amendment ${body.amendmentNo} issued to ${body.invitations.length} vendor${body.invitations.length === 1 ? "" : "s"}.`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(null);
    }
  }

  async function acknowledge(no: number, vendorId: string) {
    setBusy(`${no}-${vendorId}`);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await acknowledgeRFQAmendment(rfq.id, no, vendorId);
      // The version does not change, so keep the ETag-bearing copy the page already has.
      onChanged({ ...rfq, amendments: body.amendments });
      setNotice(`Acknowledgement from vendor ${vendorId.slice(0, 8)} recorded.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<RFQAmendment>[] = [
    { key: "ref", header: "Addendum", mono: true, render: (a) => amendmentRef(rfq, a) },
    { key: "summary", header: "Change" },
    {
      key: "deadline",
      header: "Deadline",
      mono: true,
      render: (a) => (a.newDeadline ? `${formatInstant(a.previousDeadline ?? "")} → ${formatInstant(a.newDeadline)}` : "Unchanged"),
    },
    {
      key: "lateCleared",
      header: "Late replies now on time",
      render: (a) => {
        const ids = a.lateClearedVendorIds ?? [];
        return ids.length === 0 ? "None" : ids.map((id) => id.slice(0, 8)).join(", ");
      },
    },
    { key: "criteriaChanged", header: "Criteria", render: (a) => (a.criteriaChanged ? "Replaced" : "Unchanged") },
    { key: "issuedAt", header: "Issued", mono: true, render: (a) => formatInstant(a.issuedAt) },
    {
      key: "acks",
      header: "Acknowledged",
      render: (a) => {
        const done = a.acknowledgements.filter((x) => x.acknowledgedAt).length;
        const pending = a.acknowledgements.filter((x) => !x.acknowledgedAt);
        return (
          <span className={styles.stack}>
            <span>
              {done} of {a.acknowledgements.length}
            </span>
            {pending.map((p) => (
              <Button
                key={p.vendorId}
                size="sm"
                variant="ghost"
                loading={busy === `${a.amendmentNo}-${p.vendorId}`}
                onClick={() => void acknowledge(a.amendmentNo, p.vendorId)}
              >
                Record acknowledgement from {p.vendorId.slice(0, 8)}
              </Button>
            ))}
          </span>
        );
      },
    },
  ];

  if (!open && amendments.length === 0) return null;
  return (
    <div className={styles.stack}>
      <DataTable
        label="Amendments"
        columns={columns}
        rows={amendments}
        getRowKey={(a) => String(a.amendmentNo)}
        empty={{ title: "No amendments", description: "An amendment tells the invited vendors what changed and can move the reply deadline." }}
      />
      <Alert tone="danger" live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {open && !allowed ? (
        <p className={styles.muted}>The type of this RFQ does not allow amendments.</p>
      ) : null}
      {open && allowed ? (
        <form className={styles.form} onSubmit={onSubmit} noValidate aria-label="New amendment">
          <Card title="New amendment">
            <div className={styles.grid}>
              <FormField id="rfq-amend-summary" label="What changed" error={errors.summary}>
                {(c) => <Textarea {...c} rows={3} maxLength={2000} value={summary} onChange={(e) => setSummary(e.target.value)} />}
              </FormField>
              <FormField id="rfq-amend-deadline" label="New reply deadline" optional hint="Your local time; leave blank to keep the current deadline" error={errors.replyDeadline}>
                {(c) => <Input {...c} type="datetime-local" value={deadline} onChange={(e) => setDeadline(e.target.value)} />}
              </FormField>
            </div>
            <div className={styles.checkboxRow}>
              <Input id="rfq-amend-replace" type="checkbox" checked={replace} disabled={locked !== null} onChange={(e) => setReplace(e.target.checked)} />
              <label htmlFor="rfq-amend-replace">
                Replace the scoring criteria{locked !== null ? ` (not possible: ${locked})` : ""}
              </label>
            </div>
          </Card>
          {replace ? <CriteriaEditor id="rfq-amend" criteria={criteria} onChange={setCriteria} errors={errors} /> : null}
          <div className={styles.actions}>
            <Button type="submit" variant="secondary" loading={busy === "amend"}>
              Issue amendment
            </Button>
          </div>
        </form>
      ) : null}
    </div>
  );
}
