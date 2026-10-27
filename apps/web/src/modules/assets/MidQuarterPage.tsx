import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { getMidQuarterTest, type MidQuarterBook, type MidQuarterFilter } from "./api/client";
import { money, percent, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface MidQuarterValues {
  legalEntityId: string;
  bookCode: string;
  year: string;
}

type MidQuarterErrors = Partial<Record<keyof MidQuarterValues, string>>;

/** Checks that mirror the service (internal/api midQuarterTest). */
export function validateMidQuarter(values: MidQuarterValues): { errors: MidQuarterErrors; filter?: MidQuarterFilter } {
  const errors: MidQuarterErrors = {};
  const code = values.bookCode.trim();
  const year = values.year.trim();
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!/^[A-Z0-9][A-Z0-9_-]{0,19}$/.test(code)) errors.bookCode = "Enter the tax book code in capitals, such as TAX.";
  if (!/^\d{4}$/.test(year) || Number(year) < 1900 || Number(year) > 2200) errors.year = "Enter a four-digit tax year.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, filter: { legalEntityId: values.legalEntityId.trim(), bookCode: code, year: Number(year) } };
}

function reviewColumns(currency: string, applies: boolean): DataTableColumn<MidQuarterBook>[] {
  return [
    { key: "assetNumber", header: "Asset", mono: true },
    { key: "inServiceDate", header: "In service", mono: true },
    { key: "quarter", header: "Quarter", align: "end", render: (b) => `Q${b.quarter}` },
    { key: "method", header: "Method", render: (b) => sentence(b.method) },
    { key: "convention", header: "Convention", render: (b) => sentence(b.convention) },
    { key: "cost", header: "Cost", align: "end", render: (b) => money(b.cost, currency) },
    { key: "bookId", header: "Action", render: () => (applies ? "Switch to mid-quarter" : "Switch off mid-quarter") },
  ];
}

function MidQuarterResult({ filter }: { filter: MidQuarterFilter }) {
  const navigate = useNavigate();
  const state = useLoad((signal) => getMidQuarterTest(filter, signal), JSON.stringify(filter));
  if (state.error) {
    return (
      <Alert tone="danger" title="Could not run the mid-quarter test" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        {state.error.message} ({state.error.code})
      </Alert>
    );
  }
  if (!state.data) {
    return (
      <div aria-busy="true">
        <span className="visually-hidden">Running the test…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const r = state.data;
  if (r.bookCount === 0) {
    return <EmptyState title="Nothing placed in service" description={`No ${r.bookCode} book of this legal entity was placed in service in ${r.year}.`} />;
  }
  const currency = r.currency ?? "";
  return (
    <div className={styles.stack}>
      {r.applies ? (
        <Alert tone="warning" title="Mid-quarter applies" live="status">
          {`Q4 placed-in-service share ${percent(r.q4Share)} exceeds 40%; every ${r.bookCode} asset placed in service in ${r.year} uses mid-quarter (AST-0511). Review the books below before the delta posts.`}
        </Alert>
      ) : (
        <Alert tone="success" title="Mid-quarter does not apply" live="status">
          {`Q4 placed-in-service share ${percent(r.q4Share)} is 40% or less; the books placed in service in ${r.year} keep their own conventions, and none should use mid-quarter.`}
        </Alert>
      )}
      <StatGroup label="Basis by quarter">
        {r.quarterBasis.map((basis, i) => (
          <StatTile key={i} label={`Q${i + 1} basis`} value={money(basis, currency)} hint={i === 3 ? `${percent(r.q4Share)} of the year` : undefined} />
        ))}
        <StatTile label="Year basis" value={money(r.totalBasis, currency)} />
        <StatTile label="Books" value={r.bookCount} />
      </StatGroup>
      <Card title="Books to review" actions={<Badge tone={r.reviewCount > 0 ? "warning" : "success"}>{`${r.reviewCount} to review`}</Badge>}>
        <DataTable
          label="Books to review"
          columns={reviewColumns(currency, r.applies)}
          rows={r.review}
          getRowKey={(b) => b.bookId}
          empty={{ title: "Every convention follows the test", description: "No book of the year needs a convention change." }}
          onRowOpen={(b) => navigate(`/assets/${b.assetId}`)}
        />
        {r.reviewCount > r.review.length ? <p className={styles.note}>{`Showing the first ${r.review.length} of ${r.reviewCount} books.`}</p> : null}
      </Card>
    </div>
  );
}

/** BR-37.5 mid-quarter test for one tax book and year, run on demand; nothing is switched or posted. */
export function MidQuarterPage() {
  const [values, setValues] = useState<MidQuarterValues>(() => ({ legalEntityId: "", bookCode: "TAX", year: String(new Date().getUTCFullYear()) }));
  const [errors, setErrors] = useState<MidQuarterErrors>({});
  const [filter, setFilter] = useState<MidQuarterFilter | null>(null);

  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, filter: next } = validateMidQuarter(values);
    setErrors(found);
    if (next) setFilter(next);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Mid-quarter test"
        description="When more than 40% of a tax year's placed-in-service basis falls in the fourth quarter, every asset of that year must use the mid-quarter convention."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Mid-quarter test">
          <FormField id="mq-legalEntityId" label="Legal entity" hint="UUID of the legal entity." error={errors.legalEntityId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.legalEntityId}
                onChange={(e) => setValues((v) => ({ ...v, legalEntityId: e.target.value }))}
                invalid={Boolean(errors.legalEntityId)}
              />
            )}
          </FormField>
          <FormField id="mq-bookCode" label="Tax book" error={errors.bookCode}>
            {(control) => (
              <Input {...control} className={styles.mono} value={values.bookCode} onChange={(e) => setValues((v) => ({ ...v, bookCode: e.target.value }))} invalid={Boolean(errors.bookCode)} />
            )}
          </FormField>
          <FormField id="mq-year" label="Tax year" hint="Calendar year." error={errors.year}>
            {(control) => (
              <Input {...control} inputMode="numeric" value={values.year} onChange={(e) => setValues((v) => ({ ...v, year: e.target.value }))} invalid={Boolean(errors.year)} />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Run test
            </Button>
          </div>
        </form>
      </Card>
      {filter ? <MidQuarterResult filter={filter} /> : <EmptyState title="Choose a tax book and year" description="Run the test to see the basis placed in service by quarter." />}
    </div>
  );
}
