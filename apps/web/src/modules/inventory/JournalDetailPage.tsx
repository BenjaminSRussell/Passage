import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
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
  postJournal,
  type GLLine,
  type Journal,
  type JournalLine,
  type LineDimension,
  type Problem,
} from "./api/client";
import { dash, DECIMAL, describeDimensions, formatDateTime, sentence, statusTone, typeLabels, UUID } from "./format";
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
  dimensions: { site: "", warehouse: "", location: "", batch: "", serial: "", inventory_status: "" },
});

/** Client-side checks that mirror the service's line rules (site mandatory, serial ±1). */
export function validateLine(values: LineValues): LineErrors {
  const errors: LineErrors = {};
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  const qty = values.qty.trim();
  if (!DECIMAL.test(qty) || Number(qty) === 0) {
    errors.qty = "Enter a non-zero quantity, such as 12 or -3.5.";
  } else if (values.dimensions.serial.trim() !== "" && Math.abs(Number(qty)) !== 1) {
    errors.qty = "A serial number moves one unit at a time: use 1 or -1.";
  }
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
    const found = validateLine(values);
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
          dimensions: values.dimensions,
        },
      ]);
      setValues({ ...emptyLine(), enteredUnit: values.enteredUnit, dimensions: { ...emptyLine().dimensions, site: values.dimensions.site, warehouse: values.dimensions.warehouse } });
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

  return (
    <Card title="Add line">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="line-product" label="Product" error={errors.productId}>
          {(control) => <Input {...control} value={values.productId} onChange={set("productId")} invalid={Boolean(errors.productId)} />}
        </FormField>
        <FormField id="line-qty" label="Quantity" hint="Positive receives stock, negative issues it." error={errors.qty}>
          {(control) => <Input {...control} inputMode="decimal" value={values.qty} onChange={set("qty")} invalid={Boolean(errors.qty)} />}
        </FormField>
        <FormField id="line-unit" label="Unit" error={errors.enteredUnit}>
          {(control) => <Input {...control} value={values.enteredUnit} maxLength={12} onChange={set("enteredUnit")} invalid={Boolean(errors.enteredUnit)} />}
        </FormField>
        <FormField id="line-cost" label="Cost price" optional hint="Per unit, for receipts. Issues use the running average." error={errors.costPrice}>
          {(control) => <Input {...control} inputMode="decimal" value={values.costPrice} onChange={set("costPrice")} invalid={Boolean(errors.costPrice)} />}
        </FormField>
        {LINE_DIMENSIONS.map((name) => (
          <FormField key={name} id={`line-${name}`} label={sentence(name)} optional={name !== "site"} error={errors[name]}>
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

function Detail({ term, children, mono = false }: { term: string; children: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** Journal detail: header, lines, add line (DRAFT), post, and the GL entry once posted. */
export function JournalDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getJournal(id, signal), id);
  const [journal, setJournal] = useState<Journal | null>(null);
  const [posting, setPosting] = useState(false);
  const [postFailure, setPostFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");

  useEffect(() => {
    setJournal(state.data);
  }, [state.data]);

  async function post() {
    if (!journal) return;
    setPosting(true);
    setPostFailure(null);
    setNotice("");
    try {
      const posted = await postJournal(journal.id, journal.version);
      setJournal(posted);
      setNotice(`Journal posted: ${posted.lineCount} ${posted.lineCount === 1 ? "line" : "lines"}.`);
    } catch (error) {
      setPostFailure(describeProblem(error));
    } finally {
      setPosting(false);
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
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${typeLabels[journal.journalType] ?? journal.journalType} journal`}
        meta={<Badge tone={statusTone(journal.status)}>{sentence(journal.status)}</Badge>}
        description={`Posting date ${journal.postingDate} · ${journal.currency} · ${journal.lines.length} ${journal.lines.length === 1 ? "line" : "lines"}`}
        actions={
          <>
            {back}
            {draft ? (
              <Button variant="primary" loading={posting} disabled={journal.lines.length === 0} onClick={post}>
                Post journal
              </Button>
            ) : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {postFailure ? (
        <Alert
          tone="danger"
          title="The journal was not posted"
          live="alert"
          action={postFailure.code === "conflict_version" ? <Button size="sm" onClick={state.reload}>Reload journal</Button> : undefined}
        >
          {postFailure.message} ({postFailure.code})
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
      {draft ? <AddLineForm journal={journal} onAdded={setJournal} /> : null}
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
