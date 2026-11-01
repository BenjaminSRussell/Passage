import { useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Badge, Button, Card, type DataTableColumn } from "../../shell/ui";
import { DATE, UUID, dash, dayOf, todayIso } from "./format";
import { closeLoanedItem, issueLoanedItem, listWorkerLoans, outstandingLoans, type LoanedItem, type NewLoanedItem } from "./api/safety";
import styles from "./pages.module.css";
import { BackLink, FormActions, humanize, ListPage, opt, Status, TextField, useAction, useSubmit } from "./parts";

function loanColumns(
  showWorker: boolean,
  busy: string | null,
  close: (item: LoanedItem, status: "RETURNED" | "LOST") => void,
): DataTableColumn<LoanedItem>[] {
  const today = todayIso();
  const worker: DataTableColumn<LoanedItem>[] = showWorker
    ? [{ key: "workerId", header: "Worker", mono: true, render: (row) => <Link to={`/hr/workers/${row.workerId}/loaned-items`}>{row.workerId.slice(0, 8)}</Link> }]
    : [];
  return [
    ...worker,
    { key: "itemDescription", header: "Item", render: (row) => `${row.itemDescription}${row.serial ? ` (${row.serial})` : ""}` },
    { key: "loanType", header: "Type", render: (row) => humanize(row.loanType) },
    { key: "issuedOn", header: "Issued", mono: true, render: (row) => dayOf(row.issuedOn) },
    {
      key: "dueOn",
      header: "Due",
      mono: true,
      render: (row) =>
        row.dueOn ? (
          <>
            {dayOf(row.dueOn)} {row.status === "OUT" && row.dueOn < today ? <Badge tone="danger">Overdue</Badge> : null}
          </>
        ) : (
          dash
        ),
    },
    { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.status === "OUT" ? (
          <div className={styles.rowActions}>
            {(["RETURNED", "LOST"] as const).map((status) => (
              <Button
                key={status}
                size="sm"
                loading={busy === `${row.id}|${status}`}
                disabled={busy !== null}
                aria-label={`Record ${row.itemDescription} ${status.toLowerCase()}`}
                onClick={() => close(row, status)}
              >
                {status === "RETURNED" ? "Returned" : "Lost"}
              </Button>
            ))}
          </div>
        ) : null,
    },
  ];
}

/** Everything still out on loan, soonest due first. Offboarding can't finish while an item is out. */
export function OutstandingLoansPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("dueBy") ?? "";
  const dueBy = DATE.test(raw) ? raw : "";
  const [version, setVersion] = useState(0);
  const action = useAction(() => setVersion((v) => v + 1));

  const close = (item: LoanedItem, status: "RETURNED" | "LOST") =>
    void action.run(`${item.id}|${status}`, () => closeLoanedItem(item.id, { status, closedOn: todayIso() }), `${item.itemDescription} was recorded ${status.toLowerCase()}.`);

  return (
    <ListPage<LoanedItem>
      title="Outstanding loans"
      description="Items still out, soonest due first. Recording an item closes it as of today."
      label="Outstanding loans"
      noun="loans"
      load={async (_cursor, signal) => ({ items: await outstandingLoans(dueBy, signal) })}
      loadKey={`loans|${dueBy}|${version}`}
      columns={loanColumns(true, action.busy, close)}
      getRowKey={(row) => row.id}
      empty={{ title: "Nothing is out on loan", description: dueBy ? "Nothing falls due by this date." : "Issued items appear here until they are returned." }}
      banner={action.feedback}
      toolbar={<TextField id="loans-dueBy" label="Due by" optional type="date" value={dueBy} onChange={(v) => setParams(v ? { dueBy: v } : {}, { replace: true })} />}
    />
  );
}

export interface LoanValues {
  loanType: string;
  itemDescription: string;
  serial: string;
  issuedOn: string;
  dueOn: string;
  conditionOut: string;
  legalEntityId: string;
}

export function validateLoan(v: LoanValues): { errors: Record<string, string>; input?: NewLoanedItem } {
  const errors: Record<string, string> = {};
  const loanType = v.loanType.trim().toUpperCase();
  if (!/^[A-Z0-9][A-Z0-9_-]{0,19}$/.test(loanType)) errors.loanType = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const item = v.itemDescription.trim();
  if (item === "" || item.length > 200) errors.itemDescription = "Describe the item in up to 200 characters.";
  if (!DATE.test(v.issuedOn)) errors.issuedOn = "Choose the issue date.";
  if (v.dueOn !== "" && !DATE.test(v.dueOn)) errors.dueOn = "Choose a due date, or clear it.";
  else if (v.dueOn !== "" && DATE.test(v.issuedOn) && v.dueOn < v.issuedOn) errors.dueOn = "It can't be due before it was issued.";
  if (v.legalEntityId.trim() !== "" && !UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's id.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { loanType, itemDescription: item, serial: opt(v.serial), issuedOn: v.issuedOn, dueOn: opt(v.dueOn), conditionOut: opt(v.conditionOut), legalEntityId: opt(v.legalEntityId) },
  };
}

function IssueForm({ workerId, onIssued }: { workerId: string; onIssued(item: LoanedItem): void }) {
  const [values, setValues] = useState<LoanValues>({ loanType: "", itemDescription: "", serial: "", issuedOn: todayIso(), dueOn: "", conditionOut: "", legalEntityId: "" });
  const form = useSubmit({ "HCM-ENTITY-REQUIRED": "legalEntityId", "HCM-ENTITY-MISMATCH": "legalEntityId", "HCM-LOAN-DATE": "issuedOn" });
  const field = (name: keyof LoanValues, label: string, extra?: { hint?: string; optional?: boolean; type?: string }) => (
    <TextField
      id={`loan-${name}`}
      label={label}
      value={values[name]}
      error={form.errors[name]}
      onChange={(v) => {
        setValues({ ...values, [name]: v });
        form.touch();
      }}
      {...extra}
    />
  );

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateLoan(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => issueLoanedItem(workerId, input, key), (item) => {
      setValues({ ...values, itemDescription: "", serial: "", conditionOut: "" });
      onIssued(item);
    });
  }

  return (
    <Card title="Lend an item">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Lend an item">
        {field("loanType", "Loan type", { hint: "For example LAPTOP or BADGE." })}
        {field("itemDescription", "Item")}
        {field("serial", "Serial number", { optional: true })}
        {field("issuedOn", "Issued on", { type: "date" })}
        {field("dueOn", "Due on", { optional: true, type: "date" })}
        {field("conditionOut", "Condition", { optional: true })}
        {field("legalEntityId", "Legal entity id", { optional: true, hint: "Needed when the worker is employed in several legal entities." })}
        <FormActions errors={form.errors} busy={form.busy} submit="Lend item" failure={form.failure} failureTitle="Could not lend the item" />
      </form>
    </Card>
  );
}

/** One worker's loaned items, with a form to lend another. */
export function WorkerLoansPage() {
  const { id = "" } = useParams();
  const [version, setVersion] = useState(0);
  const action = useAction(() => setVersion((v) => v + 1));

  const close = (item: LoanedItem, status: "RETURNED" | "LOST") =>
    void action.run(`${item.id}|${status}`, () => closeLoanedItem(item.id, { status, closedOn: todayIso() }), `${item.itemDescription} was recorded ${status.toLowerCase()}.`);

  return (
    <ListPage<LoanedItem>
      title="Loaned items"
      description="Company property issued to this worker."
      label="Loaned items"
      noun="loans"
      load={async (_cursor, signal) => ({ items: await listWorkerLoans(id, signal) })}
      loadKey={`worker-loans|${id}|${version}`}
      columns={loanColumns(false, action.busy, close)}
      getRowKey={(row) => row.id}
      empty={{ title: "Nothing lent to this worker", description: "Use the form to record an item you hand over." }}
      banner={
        <>
          {action.feedback}
          <IssueForm workerId={id} onIssued={() => setVersion((v) => v + 1)} />
        </>
      }
      actions={<BackLink to={`/hr/workers/${id}`}>Back to the worker</BackLink>}
    />
  );
}
