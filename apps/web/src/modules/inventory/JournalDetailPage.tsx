import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  addLines,
  describeProblem,
  getJournal,
  LINE_DIMENSIONS,
  moveJournal,
  postJournal,
  reverseJournal,
  validateJournal,
  type GLLine,
  type Journal,
  type JournalLine,
  type JournalStep,
  type JournalType,
  type LineDimension,
  type Problem,
} from "./api/client";
import { dash, DECIMAL, describeDimensions, formatDateTime, sentence, shortId, statusTone, today, typeLabels, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const lineColumns: DataTableColumn<JournalLine>[] = [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "productId", header: "Product", mono: true },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, LINE_DIMENSIONS) },
  { key: "qty", header: "Quantity", align: "end", mono: true },
  { key: "enteredUnit", header: "Unit" },
  { key: "costPrice", header: "Cost price", align: "end", mono: true },
];

const glColumns: DataTableColumn<GLLine>[] = [
  { key: "account", header: "Account", mono: true },
  { key: "debit", header: "Debit", align: "end", mono: true, render: (row) => (Number(row.debit) === 0 ? dash : row.debit) },
  { key: "credit", header: "Credit", align: "end", mono: true, render: (row) => (Number(row.credit) === 0 ? dash : row.credit) },
];

interface LineValues {
  productId: string;
  qty: string;
  enteredUnit: string;
  costPrice: string;
  dimensions: Record<LineDimension, string>;
}

type LineErrors = Partial<Record<"productId" | "qty" | "enteredUnit" | "costPrice" | LineDimension, string>>;

const emptyLine = (): LineValues => ({
  productId: "",
  qty: "",
  enteredUnit: "EA",
  costPrice: "",
  dimensions: { site: "", warehouse: "", location: "", batch: "", serial: "", inventory_status: "", owner: "", segment: "" },
});

/** Dimensions the add-line form offers: owner only on an ownership change (FR-16.17). */
function lineDimensionsFor(type: JournalType): readonly LineDimension[] {
  return type === "OWNERSHIP_CHANGE" ? LINE_DIMENSIONS : LINE_DIMENSIONS.filter((name) => name !== "owner");
}

/**
 * Client-side checks that mirror the service's line rules: site mandatory,
 * serial ±1, segment format, and for an ownership change the vendor that owns
 * the stock and a positive quantity.
 */
export function validateLine(values: LineValues, type: JournalType = "MOVEMENT"): LineErrors {
  const errors: LineErrors = {};
  const ownership = type === "OWNERSHIP_CHANGE";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  const qty = values.qty.trim();
  if (!DECIMAL.test(qty) || Number(qty) === 0 || (ownership && Number(qty) < 0)) {
    errors.qty = ownership ? "Enter the quantity to take into company ownership (above zero)." : "Enter a non-zero quantity, such as 12 or -3.5.";
  } else if (values.dimensions.serial.trim() !== "" && Math.abs(Number(qty)) !== 1) {
    errors.qty = "A serial number moves one unit at a time: use 1 or -1.";
  }
  if (ownership && values.dimensions.owner.trim() === "") errors.owner = "Enter the vendor that owns the consignment stock.";
  const segment = values.dimensions.segment.trim();
  if (segment !== "" && !/^(SALES_ORDER|PROJECT):\S.*$/.test(segment)) errors.segment = "Use SALES_ORDER:reference or PROJECT:reference.";
  if (!/^[A-Z0-9_-]{1,12}$/.test(values.enteredUnit.trim())) errors.enteredUnit = "Enter a unit code such as EA.";
  const cost = values.costPrice.trim();
  if (cost !== "" && (!DECIMAL.test(cost) || Number(cost) < 0)) errors.costPrice = "Enter a cost of zero or more.";
  if (values.dimensions.site.trim() === "") errors.site = "Site is required on every line.";
  return errors;
}

function AddLineForm({ journal, onAdded }: { journal: Journal; onAdded(updated: Journal): void }) {
  const [values, setValues] = useState<LineValues>(emptyLine);
  const [errors, setErrors] = useState<LineErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLine(values, journal.journalType);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const updated = await addLines(journal.id, [
        {
          productId: values.productId.trim(),
          qty: values.qty.trim(),
          enteredUnit: values.enteredUnit.trim(),
          costPrice: values.costPrice,
          dimensions: journal.journalType === "OWNERSHIP_CHANGE" ? values.dimensions : { ...values.dimensions, owner: "" },
        },
      ]);
      setValues({
        ...emptyLine(),
        enteredUnit: values.enteredUnit,
        dimensions: { ...emptyLine().dimensions, site: values.dimensions.site, warehouse: values.dimensions.warehouse, owner: values.dimensions.owner },
      });
      onAdded(updated);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const set = (name: "productId" | "qty" | "enteredUnit" | "costPrice") => (event: { target: { value: string } }) =>
    setValues({ ...values, [name]: name === "enteredUnit" ? event.target.value.toUpperCase() : event.target.value });
  const setDim = (name: LineDimension) => (event: { target: { value: string } }) =>
    setValues({ ...values, dimensions: { ...values.dimensions, [name]: event.target.value } });
  const ownership = journal.journalType === "OWNERSHIP_CHANGE";

  return (
    <Card title="Add line">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="line-product" label="Product" error={errors.productId}>
          {(control) => <Input {...control} value={values.productId} onChange={set("productId")} invalid={Boolean(errors.productId)} />}
        </FormField>
        <FormField
          id="line-qty"
          label="Quantity"
          hint={ownership ? "Quantity taken from the vendor into company ownership." : "Positive receives stock, negative issues it."}
          error={errors.qty}
        >
          {(control) => <Input {...control} inputMode="decimal" value={values.qty} onChange={set("qty")} invalid={Boolean(errors.qty)} />}
        </FormField>
        <FormField id="line-unit" label="Unit" error={errors.enteredUnit}>
          {(control) => <Input {...control} value={values.enteredUnit} maxLength={12} onChange={set("enteredUnit")} invalid={Boolean(errors.enteredUnit)} />}
        </FormField>
        <FormField
          id="line-cost"
          label={ownership ? "Consignment price" : "Cost price"}
          optional
          hint={ownership ? "Per unit; the company stock is valued at it." : "Per unit, for receipts. Issues use the running average."}
          error={errors.costPrice}
        >
          {(control) => <Input {...control} inputMode="decimal" value={values.costPrice} onChange={set("costPrice")} invalid={Boolean(errors.costPrice)} />}
        </FormField>
        {lineDimensionsFor(journal.journalType).map((name) => (
          <FormField
            key={name}
            id={`line-${name}`}
            label={name === "owner" ? "Owner (vendor)" : sentence(name)}
            optional={name !== "site" && name !== "owner"}
            error={errors[name]}
          >
            {(control) => <Input {...control} value={values.dimensions[name]} maxLength={60} onChange={setDim(name)} invalid={Boolean(errors[name])} />}
          </FormField>
        ))}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not add the line" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Add line
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Creates the DRAFT reversal of a posted journal and opens it (BR-16.12). */
function ReverseForm({ journal, onCancel }: { journal: Journal; onCancel(): void }) {
  const navigate = useNavigate();
  const [date, setDate] = useState(today());
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) {
      setError("Enter the posting date of the reversal.");
      return;
    }
    setError("");
    setFailure(null);
    setBusy(true);
    try {
      const reversal = await reverseJournal(journal.id, date);
      navigate(`/inventory/journals/${reversal.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      setBusy(false);
    }
  }

  return (
    <Card title="Reverse journal">
      <form onSubmit={submit} noValidate className={styles.form}>
        <p className={`${styles.muted} ${styles.formActions}`}>
          Creates a draft journal with every line negated. Posting it takes each line out at the value the original posted, so the GL entry mirrors the original.
        </p>
        <FormField id="reverse-date" label="Posting date" error={error}>
          {(control) => <Input {...control} type="date" value={date} onChange={(e) => setDate(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The journal was not reversed" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Create reversal
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}

function Detail({ term, children, mono = false }: { term: string; children: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

type Action = "validate" | "post" | JournalStep;

const stepNotices: Record<JournalStep, string> = {
  submit: "Journal submitted for approval.",
  approve: "Journal approved. It can now be posted.",
  reject: "Journal rejected.",
  reopen: "Journal reopened as a draft.",
  cancel: "Journal cancelled.",
};

const failureTitles: Record<Action, string> = {
  validate: "The journal would not post",
  post: "The journal was not posted",
  submit: "The journal was not submitted",
  approve: "The journal was not approved",
  reject: "The journal was not rejected",
  reopen: "The journal was not reopened",
  cancel: "The journal was not cancelled",
};

/**
 * Journal detail: header, lines, add line (DRAFT), the approval workflow of
 * BR-16.11 (validate, submit, approve or reject, reopen, cancel), post,
 * reverse (POSTED), and the GL entry once posted.
 */
export function JournalDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getJournal(id, signal), id);
  const [journal, setJournal] = useState<Journal | null>(null);
  const [busy, setBusy] = useState<Action | "">("");
  const [failure, setFailure] = useState<{ action: Action; problem: Problem } | null>(null);
  const [notice, setNotice] = useState("");
  const [reversing, setReversing] = useState(false);

  useEffect(() => {
    setJournal(state.data);
    setReversing(false);
  }, [state.data]);

  async function act(action: Action) {
    if (!journal) return;
    setBusy(action);
    setFailure(null);
    setNotice("");
    try {
      if (action === "validate") {
        const result = await validateJournal(journal.id);
        setNotice(`The journal would post now: ${result.lineCount} ${result.lineCount === 1 ? "line" : "lines"} passed every posting check.`);
      } else if (action === "post") {
        const posted = await postJournal(journal.id, journal.version);
        setJournal(posted);
        setNotice(`Journal posted: ${posted.lineCount} ${posted.lineCount === 1 ? "line" : "lines"}.`);
      } else {
        const moved = await moveJournal(journal.id, journal.version, action);
        setJournal(moved);
        setNotice(stepNotices[action]);
      }
    } catch (error) {
      setFailure({ action, problem: describeProblem(error) });
    } finally {
      setBusy("");
    }
  }

  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/journals">
      Back to journals
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-JNL-NOT-FOUND") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Journal not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return (
      <Alert tone="danger" title="Could not load the journal" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        {state.error.message} ({state.error.code})
      </Alert>
    );
  }
  if (!journal) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading journal…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const draft = journal.status === "DRAFT";
  const reversal = Boolean(journal.reversesJournalId);
  const canReverse = journal.status === "POSTED" && !journal.reversedByJournalId && journal.journalType !== "OWNERSHIP_CHANGE";
  const hasLines = journal.lines.length > 0;
  const button = (action: Action, label: string, variant: "primary" | "secondary" | "danger" = "secondary", disabled = false) => (
    <Button variant={variant} loading={busy === action} disabled={busy !== "" || disabled} onClick={() => act(action)}>
      {label}
    </Button>
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${typeLabels[journal.journalType] ?? journal.journalType} journal`}
        meta={<Badge tone={statusTone(journal.status)}>{sentence(journal.status)}</Badge>}
        description={`Posting date ${journal.postingDate} · ${journal.currency} · ${journal.lines.length} ${journal.lines.length === 1 ? "line" : "lines"}`}
        actions={
          <>
            {back}
            {draft || journal.status === "APPROVED" || journal.status === "SUBMITTED" ? button("validate", "Validate", "secondary", !hasLines) : null}
            {draft ? button("submit", "Submit for approval", "secondary", !hasLines) : null}
            {journal.status === "SUBMITTED" ? button("reject", "Reject", "danger") : null}
            {journal.status === "SUBMITTED" ? button("approve", "Approve", "primary") : null}
            {journal.status === "REJECTED" ? button("reopen", "Reopen as draft") : null}
            {(draft && !reversal) || journal.status === "REJECTED" ? button("cancel", "Cancel journal", "danger") : null}
            {draft || journal.status === "APPROVED" ? button("post", reversal ? "Post reversal" : "Post journal", "primary", !hasLines) : null}
            {canReverse && !reversing ? <Button onClick={() => setReversing(true)}>Reverse journal</Button> : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={failureTitles[failure.action]}
          live="alert"
          action={failure.problem.code === "conflict_version" ? <Button size="sm" onClick={state.reload}>Reload journal</Button> : undefined}
        >
          {failure.problem.message} ({failure.problem.code})
        </Alert>
      ) : null}
      {journal.status === "SUBMITTED" ? (
        <Alert tone="info" title="Awaiting approval">
          A submitted journal takes no lines and cannot be posted until an approver approves it.
        </Alert>
      ) : null}
      {journal.status === "REJECTED" ? (
        <Alert tone="warning" title="Rejected">
          Reopen the journal as a draft to correct it, or cancel it.
        </Alert>
      ) : null}
      {reversing ? <ReverseForm journal={journal} onCancel={() => setReversing(false)} /> : null}
      {journal.reversesJournalId ? (
        <Alert tone="info" title={draft ? "Reversal draft" : "Reversal"}>
          This journal reverses journal <Link to={`/inventory/journals/${journal.reversesJournalId}`}>{shortId(journal.reversesJournalId)}</Link>.
          {draft ? " It holds exactly the negated lines; post it to take the movement back out." : ""}
        </Alert>
      ) : null}
      {journal.reversedByJournalId ? (
        <Alert tone="info" title="Reversed">
          Reversed by journal <Link to={`/inventory/journals/${journal.reversedByJournalId}`}>{shortId(journal.reversedByJournalId)}</Link>.
        </Alert>
      ) : null}
      <Card title="Details">
        <dl className={styles.details}>
          <Detail term="Journal id" mono>
            {journal.id}
          </Detail>
          <Detail term="Legal entity" mono>
            {journal.legalEntityId}
          </Detail>
          <Detail term="Offset account" mono>
            {journal.offsetAccountId || "Posting profile"}
          </Detail>
          <Detail term="Version">{String(journal.version)}</Detail>
          <Detail term="Created">{formatDateTime(journal.createdAt)}</Detail>
          <Detail term="Posted">{formatDateTime(journal.postedAt)}</Detail>
        </dl>
      </Card>
      <DataTable
        label="Journal lines"
        columns={lineColumns}
        rows={journal.lines}
        getRowKey={(row) => row.id}
        empty={{ title: "No lines yet", description: draft ? "Add a line below, then post the journal." : "This journal has no lines." }}
      />
      {draft && !reversal ? <AddLineForm journal={journal} onAdded={setJournal} /> : null}
      {journal.glEntry ? (
        <Card title="GL entry">
          <div className={styles.stack}>
            <p className={styles.muted}>
              {journal.glEntry.ledgerEntryId
                ? `Ledger voucher ${journal.glEntry.ledgerEntryId}`
                : "Balanced entry built at posting. It is sent to the general ledger once the ledger service is live."}
            </p>
            <DataTable
              label="GL entry"
              columns={glColumns}
              rows={journal.glEntry.lines}
              getRowKey={(row, i) => `${row.account}-${i}`}
              empty={{ title: "No value posted", description: "Every line on this journal had zero cost." }}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
