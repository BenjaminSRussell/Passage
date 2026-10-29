import { useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  StatGroup,
  StatTile,
  Textarea,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  importCardFeed,
  listCardTransactions,
  newIdempotencyKey,
  type CardImport,
  type CardImportInput,
  type CardTxnStatus,
  type FeedFormat,
  type StoredCardTransaction,
} from "./api/client";
import { splitProblem } from "./format";
import { amount, usePaged, UUID, type Failure } from "./persisted";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

/** The service's feed limit (2 MiB) and cardholder map limit. */
const MAX_FEED_CHARS = 2 * 1024 * 1024;
const MAX_CARDHOLDERS = 5000;
const LAST4 = /^\d{4}$/;

export const CARD_STATUS: Record<CardTxnStatus, { label: string; tone: BadgeTone }> = {
  UNASSIGNED: { label: "Unassigned", tone: "warning" },
  ASSIGNED: { label: "On a report", tone: "success" },
  DISPUTED: { label: "Disputed", tone: "danger" },
  PERSONAL: { label: "Personal", tone: "neutral" },
};

const txnColumns: DataTableColumn<StoredCardTransaction>[] = [
  { key: "txnDate", header: "Date", mono: true },
  { key: "merchant", header: "Merchant" },
  { key: "cardLast4", header: "Card", mono: true, render: (t) => `•••• ${t.cardLast4}` },
  { key: "workerId", header: "Worker", mono: true },
  { key: "status", header: "Status", render: (t) => <Badge tone={CARD_STATUS[t.status]?.tone ?? "neutral"}>{CARD_STATUS[t.status]?.label ?? t.status}</Badge> },
  { key: "amount", header: "Amount", align: "end", mono: true, render: (t) => amount(t.currency, t.amount) },
];

/** FR-43.13 imported card transactions; UNASSIGNED is a worker's unassigned pool. */
export function CardTransactionsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CardTxnStatus | "">("UNASSIGNED");
  const [worker, setWorker] = useState("");
  const workerId = UUID.test(worker.trim()) ? worker.trim() : "";
  const workerError = worker.trim() !== "" && workerId === "" ? "Enter a worker id (a UUID) or leave it empty." : undefined;
  const list = usePaged((cursor) => listCardTransactions({ status: status || undefined, workerId: workerId || undefined, cursor }), `${status}|${workerId}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Card transactions"
        description="Company card spend imported from issuer statements. Unassigned transactions wait in their cardholder's pool until they are put on an expense report."
        actions={
          <Button variant="primary" icon="inbox" onClick={() => navigate("/expense/card-transactions/import")}>
            Import a statement
          </Button>
        }
      />
      <Card>
        <div className={styles.form}>
          <FormField id="cards-status" label="Status">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as CardTxnStatus | "")}>
                <option value="">All statuses</option>
                {(Object.keys(CARD_STATUS) as CardTxnStatus[]).map((s) => (
                  <option key={s} value={s}>
                    {CARD_STATUS[s].label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="cards-worker" label="Worker" optional error={workerError}>
            {(c) => <Input {...c} className={styles.mono} value={worker} onChange={(e) => setWorker(e.target.value)} invalid={Boolean(workerError)} />}
          </FormField>
        </div>
      </Card>
      {list.failure ? (
        <Alert tone="danger" title="The card transactions could not be loaded" live="alert" action={<Button size="sm" onClick={list.retry}>Try again</Button>}>
          {list.failure.message} ({list.failure.code})
        </Alert>
      ) : null}
      <DataTable
        label="Card transactions"
        columns={txnColumns}
        rows={list.items}
        loading={list.loading}
        getRowKey={(t) => t.id}
        empty={
          status || workerId
            ? { title: "No card transactions match", description: "Change the status or worker to see other transactions." }
            : { title: "No card transactions yet", description: "Import an issuer statement to fill the cardholders' pools." }
        }
      />
      {list.next && !list.loading ? (
        <div className={styles.formActions}>
          <Button loading={list.loadingMore} onClick={list.more}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/**
 * Parses the cardholder list: one card per line as `1234 <worker id>` (a space, comma,
 * equals sign or colon between them). Returns the map and the first problem found.
 */
export function parseCardholders(raw: string): { map: Record<string, string>; error?: string } {
  const map: Record<string, string> = {};
  const lines = raw.split(/\r?\n/).map((l) => l.trim()).filter((l) => l !== "");
  if (lines.length === 0) return { map, error: "List each card's last four digits with the worker id who holds it." };
  if (lines.length > MAX_CARDHOLDERS) return { map, error: `List at most ${MAX_CARDHOLDERS} cards per import.` };
  for (const [i, line] of lines.entries()) {
    const [last4, worker, ...rest] = line.split(/[\s,=:]+/);
    if (!LAST4.test(last4 ?? "") || !UUID.test(worker ?? "") || rest.length > 0) {
      return { map, error: `Line ${i + 1} must be the card's last four digits, then the worker id (a UUID).` };
    }
    if (map[last4] !== undefined && map[last4] !== worker) return { map, error: `Card ${last4} is listed for two workers.` };
    map[last4] = worker;
  }
  return { map };
}

export interface ImportValues {
  legalEntityId: string;
  format: FeedFormat;
  content: string;
  cardholders: string;
}

export function validateImport(v: ImportValues): { errors: Errors; input?: CardImportInput } {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.content.trim() === "") errors.content = "Paste the card statement or choose a file.";
  else if (v.content.length > MAX_FEED_CHARS) errors.content = "The file is larger than 2 MB. Split it and import each part.";
  const holders = parseCardholders(v.cardholders);
  if (holders.error) errors.cardholders = holders.error;
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { legalEntityId: v.legalEntityId.trim(), format: v.format, content: v.content, cardholders: holders.map } };
}

const IMPORT_FIELDS = ["legalEntityId", "format", "content", "cardholders"];

const quarantineColumns: DataTableColumn<CardImport["quarantined"][number]>[] = [
  { key: "line", header: "Line", align: "end" },
  { key: "reason", header: "Reason" },
];

/** FR-43.13 card feed import: store a CSV or OFX statement in the cardholders' pools. */
export function CardImportPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ImportValues>({ legalEntityId: "", format: "CSV", content: "", cardholders: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [result, setResult] = useState<CardImport | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newIdempotencyKey());

  function update(patch: Partial<ImportValues>) {
    key.current = newIdempotencyKey();
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function onFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    const content = await file.text();
    if (/\.(ofx|qfx)$/i.test(file.name)) update({ format: "OFX", content });
    else if (/\.csv$/i.test(file.name)) update({ format: "CSV", content });
    else update({ content });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateImport(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await importCardFeed(input, key.current));
    } catch (err) {
      const split = splitProblem(describeProblem(err), IMPORT_FIELDS);
      setResult(null);
      setErrors(split.fields);
      setFailure(split.failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Import a card statement"
        description="Each transaction goes to the unassigned pool of the worker holding the card. Rows that cannot be read, and cards with no cardholder listed, are quarantined. The same file is never imported twice."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="import-legal-entity" label="Legal entity" hint="The legal entity id; it must be in your scope." error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="import-format" label="Format" error={errors.format}>
            {(c) => (
              <Select {...c} value={values.format} onChange={(e) => update({ format: e.target.value as FeedFormat })}>
                <option value="CSV">CSV</option>
                <option value="OFX">OFX</option>
              </Select>
            )}
          </FormField>
          <FormField id="import-file" label="Statement file" optional hint="Or paste the content below.">
            {(c) => <Input {...c} type="file" accept=".csv,.ofx,.qfx,text/csv" onChange={onFile} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="import-content" label="Statement content" error={errors.content}>
              {(c) => <Textarea {...c} mono rows={8} value={values.content} onChange={(e) => update({ content: e.target.value })} invalid={Boolean(errors.content)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField id="import-cardholders" label="Cardholders" hint="One card per line: the last four digits, then the worker id who holds it." error={errors.cardholders}>
              {(c) => <Textarea {...c} mono rows={4} value={values.cardholders} onChange={(e) => update({ cardholders: e.target.value })} invalid={Boolean(errors.cardholders)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The statement was not imported" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Import statement
            </Button>
            <Button variant="ghost" onClick={() => navigate("/expense/card-transactions")}>
              Card transactions
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            <Alert tone="success" title={`${result.imported} transactions imported`} live="status" />
            <StatGroup label="Import summary">
              <StatTile label="Imported" value={result.imported} />
              <StatTile label="Quarantined" value={result.quarantined.length} tone="danger" />
            </StatGroup>
            <p className={styles.note}>
              {`${result.format} file, SHA-256 `}
              <span className={styles.hash}>{result.fileHash}</span>
            </p>
            {result.quarantined.length > 0 ? (
              <DataTable label="Quarantined rows" columns={quarantineColumns} rows={result.quarantined} getRowKey={(r, i) => `${r.line}-${i}`} />
            ) : null}
          </div>
        ) : (
          <EmptyState icon="inbox" title="Nothing imported yet" description="Choose or paste a statement, list the cardholders, then import." compact />
        )}
      </Card>
    </div>
  );
}
