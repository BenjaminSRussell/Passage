import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, PageHeader, type DataTableColumn, DataTable } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { getWorksheet, saveWorksheet, submitWorksheet, type WorksheetInput, type WorksheetRow } from "./api/recruitment";
import { UUID, compareDecimal, dash, validSigned } from "./format";
import styles from "./pages.module.css";
import { BackLink, ConfirmAction, Loaded, LookupField, ProblemAlert, Status, TextField } from "./parts";
import { useLoad } from "./useLoad";

interface Line {
  workerId: string;
  rating: string;
  proposedPct: string;
  justification: string;
}

const toLine = (row: WorksheetRow): Line => ({ workerId: row.workerId, rating: String(row.rating), proposedPct: row.proposedPct, justification: row.justification ?? "" });
const MAX_ROWS = 200;

type LineField = keyof Line;

export function validateWorksheet(lines: Line[]): { errors: Record<number, string>; where: Record<number, LineField>; rows?: WorksheetInput[] } {
  const errors: Record<number, string> = {};
  const where: Record<number, LineField> = {};
  const seen = new Set<string>();
  lines.forEach((line, i) => {
    const worker = line.workerId.trim().toLowerCase();
    const rating = Number(line.rating);
    const fail = (field: LineField, message: string) => {
      errors[i] = message;
      where[i] = field;
    };
    if (!UUID.test(worker)) fail("workerId", "Enter the worker's id.");
    else if (seen.has(worker)) fail("workerId", "This worker is already on the worksheet.");
    else if (!Number.isInteger(rating) || rating < 1 || rating > 5) fail("rating", "The rating is 1 to 5.");
    else if (!validSigned(line.proposedPct, 3) || compareDecimal(line.proposedPct, "-100") < 0 || compareDecimal(line.proposedPct, "100") > 0)
      fail("proposedPct", "The raise is a number from -100 to 100 with up to three decimals.");
    else if (line.justification.trim().length > 1000) fail("justification", "Use up to 1000 characters for the justification.");
    seen.add(worker);
  });
  if (lines.length === 0) errors[-1] = "Add at least one worker.";
  if (Object.keys(errors).length > 0) return { errors, where };
  return {
    errors,
    where,
    rows: lines.map((l) => ({
      workerId: l.workerId.trim().toLowerCase(),
      rating: Number(l.rating),
      proposedPct: l.proposedPct.trim(),
      justification: l.justification.trim() === "" ? undefined : l.justification.trim(),
    })),
  };
}

const resultColumns: DataTableColumn<WorksheetRow>[] = [
  { key: "workerId", header: "Worker", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}`}>{row.workerId.slice(0, 8)}</Link> },
  { key: "rating", header: "Rating", align: "end" },
  { key: "currentPay", header: "Current pay", mono: true, align: "end" },
  { key: "compaRatio", header: "Compa-ratio", mono: true, align: "end", render: (row) => row.compaRatio ?? dash },
  { key: "guideline", header: "Guideline", mono: true, render: (row) => (row.guidelinePctMin !== undefined ? `${row.guidelinePctMin}% to ${row.guidelinePctMax}%` : "Outside the matrix") },
  { key: "proposedPct", header: "Proposed", mono: true, align: "end", render: (row) => `${row.proposedPct}%` },
  { key: "proposedAmount", header: "Raise", mono: true, align: "end" },
  { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
];

export function WorksheetPage() {
  const { id = "", managerId = "" } = useParams();
  const state = useLoad((signal) => getWorksheet(id, managerId, signal), `${id}|${managerId}`);
  const [rows, setRows] = useState<WorksheetRow[] | null>(null);
  const [lines, setLines] = useState<Line[]>([]);
  const [errors, setErrors] = useState<Record<number, string>>({});
  const [where, setWhere] = useState<Record<number, LineField>>({});
  const [busy, setBusy] = useState<"save" | "submit" | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    if (state.data) {
      setRows(state.data);
      setLines(state.data.map(toLine));
    }
  }, [state.data]);

  const dirty = rows !== null && JSON.stringify(lines) !== JSON.stringify(rows.map(toLine));
  const locked = rows !== null && rows.length > 0 && rows.every((r) => r.status === "SUBMITTED");
  const back = `/hr/compensation/cycles/${id}`;

  async function call(kind: "save" | "submit", run: () => Promise<WorksheetRow[]>, done: string) {
    setBusy(kind);
    setFailure(null);
    setNotice(null);
    try {
      const saved = await run();
      setRows(saved);
      setLines(saved.map(toLine));
      setNotice(done);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const result = validateWorksheet(lines);
    setErrors(result.errors);
    setWhere(result.where);
    if (!result.rows) return;
    const input = result.rows;
    await call("save", () => saveWorksheet(id, managerId, input), "The worksheet was saved as a draft.");
  }

  const update = (i: number, patch: Partial<Line>) => setLines(lines.map((l, j) => (j === i ? { ...l, ...patch } : l)));

  return (
    <Loaded state={state} title="Worksheet" what="worksheet" backTo={back}>
      {() => (
        <div className={styles.stack}>
          <PageHeader title="Manager worksheet" description={`Proposals for the reports of ${managerId.slice(0, 8)}.`} actions={<BackLink to={back}>Back to cycle</BackLink>} />
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {failure ? <ProblemAlert problem={failure} title="That didn't work" /> : null}
          {locked ? <Alert tone="info">This worksheet was submitted and can't be changed.</Alert> : null}
          {rows && rows.length > 0 ? (
            <Card title="Saved proposals">
              <DataTable label="Saved proposals" columns={resultColumns} rows={rows} getRowKey={(row) => row.workerId} />
            </Card>
          ) : null}
          {locked ? null : (
            <Card title="Edit proposals">
              <form className={styles.stack} onSubmit={save} noValidate aria-label="Edit proposals">
                {lines.length === 0 ? <p className={styles.note}>No proposals yet. Add a worker to start.</p> : null}
                {errors[-1] ? <p role="alert">{errors[-1]}</p> : null}
                {lines.map((line, i) => (
                  <div key={i} className={styles.optionRow} role="group" aria-label={`Proposal ${i + 1}`}>
                    <LookupField entity="worker" id={`ws-${i}-worker`} label="Worker" value={line.workerId} error={where[i] === "workerId" ? errors[i] : undefined} onChange={(v) => update(i, { workerId: v })} />
                    <TextField id={`ws-${i}-rating`} label="Rating (1 to 5)" value={line.rating} error={where[i] === "rating" ? errors[i] : undefined} onChange={(v) => update(i, { rating: v })} />
                    <TextField id={`ws-${i}-pct`} label="Raise (%)" value={line.proposedPct} error={where[i] === "proposedPct" ? errors[i] : undefined} onChange={(v) => update(i, { proposedPct: v })} />
                    <TextField id={`ws-${i}-why`} label="Justification" optional value={line.justification} error={where[i] === "justification" ? errors[i] : undefined} onChange={(v) => update(i, { justification: v })} />
                    <div className={styles.optionRemove}>
                      <Button variant="ghost" size="sm" aria-label={`Remove proposal ${i + 1}`} onClick={() => setLines(lines.filter((_, j) => j !== i))}>
                        Remove
                      </Button>
                    </div>
                  </div>
                ))}
                <div className={styles.rowActions}>
                  {lines.length < MAX_ROWS ? (
                    <Button onClick={() => setLines([...lines, { workerId: "", rating: "3", proposedPct: "0", justification: "" }])}>Add worker</Button>
                  ) : null}
                  <Button type="submit" variant="primary" loading={busy === "save"}>
                    Save draft
                  </Button>
                  {rows && rows.length > 0 ? (
                    <ConfirmAction
                      label="Submit worksheet"
                      variant="primary"
                      title="Submit this worksheet?"
                      description="Submitted proposals go to approval and can't be edited."
                      confirmLabel="Submit"
                      busy={busy === "submit"}
                      disabled={dirty}
                      onConfirm={() => void call("submit", () => submitWorksheet(id, managerId), "The worksheet was submitted.")}
                    />
                  ) : null}
                </div>
                {dirty ? <p className={styles.note}>Save changes first: submitting sends only the saved draft.</p> : null}
              </form>
            </Card>
          )}
        </div>
      )}
    </Loaded>
  );
}
