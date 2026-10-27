import { useState, type ChangeEvent, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, StatGroup, StatTile, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, parseCardFeed, type CardFeedResult, type CardTransaction, type FeedFormat, type Problem, type RejectedRow } from "./api/client";
import { splitProblem } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

/** Keeps the request inside the service's feed limit (2 MiB). */
const MAX_FEED_CHARS = 2 * 1024 * 1024;
const SHOWN = ["format", "content"];

export function validateFeed(content: string): Errors {
  if (content.trim() === "") return { content: "Paste the card statement or choose a file." };
  if (content.length > MAX_FEED_CHARS) return { content: "The file is larger than 2 MB. Split it and preview each part." };
  return {};
}

const txnColumns: DataTableColumn<CardTransaction>[] = [
  { key: "line", header: "Line", align: "end" },
  { key: "cardLast4", header: "Card", mono: true, render: (t) => `•••• ${t.cardLast4}` },
  { key: "txnDate", header: "Transaction date", mono: true },
  { key: "postDate", header: "Posted", mono: true },
  { key: "merchant", header: "Merchant" },
  { key: "mcc", header: "MCC", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (t) => `${t.amount} ${t.currency}` },
];

const rejectedColumns: DataTableColumn<RejectedRow>[] = [
  { key: "line", header: "Line", align: "end" },
  { key: "reason", header: "Reason" },
];

/** FR-43.13 card feed preview: parse a CSV or OFX statement and see what would import. Stores nothing. */
export function CardFeedPage() {
  const [format, setFormat] = useState<FeedFormat>("CSV");
  const [content, setContent] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Pick<Problem, "code" | "message"> | null>(null);
  const [result, setResult] = useState<CardFeedResult | null>(null);
  const [busy, setBusy] = useState(false);

  async function onFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    if (/\.(ofx|qfx)$/i.test(file.name)) setFormat("OFX");
    else if (/\.csv$/i.test(file.name)) setFormat("CSV");
    setContent(await file.text());
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFeed(content);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await parseCardFeed(format, content));
    } catch (error) {
      const split = splitProblem(describeProblem(error), SHOWN);
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
        title="Card feed preview"
        description="Check a card issuer statement before it is imported. CSV needs the columns card_last4, txn_date, post_date, merchant, amount and currency (mcc is optional); OFX statements are read as issued. Spend shows positive and refunds negative. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="feed-format" label="Format" error={errors.format}>
            {(c) => (
              <Select {...c} value={format} onChange={(e) => setFormat(e.target.value as FeedFormat)}>
                <option value="CSV">CSV</option>
                <option value="OFX">OFX</option>
              </Select>
            )}
          </FormField>
          <FormField id="feed-file" label="Statement file" optional hint="Or paste the content below.">
            {(c) => <Input {...c} type="file" accept=".csv,.ofx,.qfx,text/csv" onChange={onFile} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="feed-content" label="Statement content" error={errors.content}>
              {(c) => <Textarea {...c} mono rows={10} value={content} onChange={(e) => setContent(e.target.value)} invalid={Boolean(errors.content)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not read the statement" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview import
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            <StatGroup label="Feed summary">
              <StatTile label="Transactions" value={result.transactions.length} />
              <StatTile label="Rejected rows" value={result.rejected.length} tone="danger" />
            </StatGroup>
            <p className={styles.note}>
              {`${result.format} file, SHA-256 `}
              <span className={styles.hash}>{result.fileHash}</span>
              {". The same file is never imported twice."}
            </p>
            <DataTable
              label="Card transactions"
              columns={txnColumns}
              rows={result.transactions}
              getRowKey={(t) => `${t.line}`}
              empty={{ title: "No transactions in this file", description: "Every row was rejected, or the statement holds none." }}
            />
            {result.rejected.length > 0 ? (
              <DataTable label="Rejected rows" columns={rejectedColumns} rows={result.rejected} getRowKey={(r, i) => `${r.line}-${i}`} />
            ) : null}
          </div>
        ) : busy ? (
          <DataTable label="Card transactions" columns={txnColumns} rows={[]} getRowKey={(t) => `${t.line}`} loading />
        ) : (
          <EmptyState icon="receipt" title="No statement previewed yet" description="Choose or paste a statement, then preview the import." compact />
        )}
      </Card>
    </div>
  );
}
