import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getCommodityBasis,
  listCommodityIndexValues,
  newKey,
  recordCommodityIndexValue,
  type CommodityIndexValue,
  type Problem,
} from "./api/client";
import { DAY, DECIMAL } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const columns: DataTableColumn<CommodityIndexValue>[] = [
  { key: "asOf", header: "Day", mono: true },
  { key: "revision", header: "Revision", mono: true },
  { key: "value", header: "Index value", mono: true },
  { key: "source", header: "Source" },
  { key: "affectedAgreements", header: "Agreements affected", mono: true, align: "end", render: (q) => q.affectedAgreements ?? "" },
];

/** One cost basis type with its index quotes and the form that records the next one (FR-26.21). */
export function CommodityBasisDetailPage() {
  const { id = "" } = useParams();
  const basis = useLoad((signal) => getCommodityBasis(id, signal), id);
  const quotes = useLoad((signal) => listCommodityIndexValues(id, signal), id);
  const [asOf, setAsOf] = useState("");
  const [value, setValue] = useState("");
  const [source, setSource] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (basis.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Cost basis type" />
        <Alert tone="danger" title="Could not load the cost basis type" live="alert" action={<Button size="sm" onClick={basis.reload}>Try again</Button>}>
          <ProblemMessage problem={basis.error} />
        </Alert>
      </div>
    );
  }
  if (!basis.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Cost basis type" />
        <span className="visually-hidden">Loading the cost basis type…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const b = basis.data;

  async function record(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!DAY.test(asOf)) found.asOf = "Enter the day as YYYY-MM-DD.";
    if (!DECIMAL.test(value.trim()) || Number(value) <= 0) found.value = "Enter an index value above 0.";
    setErrors(found);
    setFailure(null);
    setDone("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await recordCommodityIndexValue(id, { asOf, value: value.trim(), source: source.trim() }, key.current);
      setDone(`Recorded ${saved.value} for ${saved.asOf} as revision ${saved.revision}.`);
      setValue("");
      key.current = newKey();
      quotes.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${b.code}: ${b.name}`}
        description={`Quoted in ${b.currency} per ${b.unit}`}
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/pricing/commodity/bases">
              All cost basis types
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/commodity/calculations/new">
              New price calculation
            </Link>
          </>
        }
      />
      <Card title="Cost basis type">
        <dl className={styles.details}>
          <Detail term="Code" mono>
            {b.code}
          </Detail>
          <Detail term="Currency" mono>
            {b.currency}
          </Detail>
          <Detail term="Quoted per" mono>
            {b.unit}
          </Detail>
          <Detail term="Legal entity" mono>
            {b.legalEntityId}
          </Detail>
          <Detail term="Id" mono>
            {b.id}
          </Detail>
        </dl>
      </Card>
      <Card title="Record an index quote">
        <form className={styles.form} onSubmit={record} noValidate>
          <FormField id="iv-day" label="Day" error={errors.asOf}>
            {(c) => <Input {...c} type="date" className={styles.mono} value={asOf} onChange={(e) => setAsOf(e.target.value)} invalid={Boolean(errors.asOf)} />}
          </FormField>
          <FormField id="iv-value" label="Index value" hint="A second quote for the same day becomes the next revision; earlier quotes are kept." error={errors.value}>
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={value} onChange={(e) => setValue(e.target.value)} invalid={Boolean(errors.value)} />}
          </FormField>
          <FormField id="iv-source" label="Source" optional>
            {(c) => <Input {...c} value={source} onChange={(e) => setSource(e.target.value)} />}
          </FormField>
          <Alert tone="success" live="status">
            {done}
          </Alert>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not record the quote" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record quote
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Index quotes">
        {quotes.error ? (
          <Alert tone="danger" title="Could not load the index quotes" live="alert" action={<Button size="sm" onClick={quotes.reload}>Try again</Button>}>
            <ProblemMessage problem={quotes.error} />
          </Alert>
        ) : (
          <DataTable
            label="Index quotes"
            columns={columns}
            rows={quotes.data ?? []}
            getRowKey={(row) => `${row.asOf}/${row.revision}`}
            loading={quotes.loading}
            empty={{ title: "No index quotes", description: "Record the first quote above." }}
          />
        )}
      </Card>
    </div>
  );
}
