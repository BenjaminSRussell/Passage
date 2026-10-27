import { useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getInsuranceReport, INSURANCE_TYPES, type InsuranceReportFilter, type InsuranceReportRow, type Problem } from "./api/client";
import { DATE, dash, money, sentence, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<InsuranceReportRow>[] = [
  { key: "assetNumber", header: "Asset", mono: true },
  { key: "assetName", header: "Name" },
  { key: "policyNumber", header: "Policy", mono: true },
  { key: "insuranceType", header: "Type", render: (r) => sentence(r.insuranceType) },
  { key: "coverageTo", header: "Cover ends", mono: true, render: (r) => (r.expiringSoon ? `${r.coverageTo} (soon)` : r.coverageTo) },
  { key: "sumInsured", header: "Insured", align: "end", render: (r) => money(r.sumInsured, r.currency) },
  { key: "indexedValue", header: "Indexed replacement", align: "end", render: (r) => (r.indexedValue ? money(r.indexedValue, r.currency) : dash) },
  { key: "books", header: "Net book value", align: "end", render: (r) => (r.books.length === 0 ? dash : r.books.map((b) => `${b.bookCode} ${money(b.nbv, b.currency)}`).join("; ")) },
  { key: "underInsured", header: "Cover", render: (r) => (r.underInsured ? <Badge tone="danger">Under-insured</Badge> : <Badge tone="success">Adequate</Badge>) },
];

export interface ReportValues {
  legalEntityId: string;
  asOf: string;
  insuranceType: InsuranceReportFilter["insuranceType"];
  expiringWithinDays: string;
}

type ReportErrors = Partial<Record<keyof ReportValues, string>>;

/** Checks that mirror the service (internal/api insuranceReport). */
export function validateReport(values: ReportValues): { errors: ReportErrors; filter?: InsuranceReportFilter } {
  const errors: ReportErrors = {};
  const days = values.expiringWithinDays.trim();
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!DATE.test(values.asOf)) errors.asOf = "Choose the report date.";
  if (days !== "" && (!/^\d+$/.test(days) || Number(days) > 366)) errors.expiringWithinDays = "Enter 0 to 366 days, or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    filter: { legalEntityId: values.legalEntityId.trim(), asOf: values.asOf, insuranceType: values.insuranceType, expiringWithinDays: days === "" ? undefined : Number(days) },
  };
}

function ReportResults({ filter }: { filter: InsuranceReportFilter }) {
  const navigate = useNavigate();
  const key = JSON.stringify(filter);
  const state = useLoad((signal) => getInsuranceReport(filter, signal), key);
  const [more, setMore] = useState<InsuranceReportRow[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await getInsuranceReport({ ...filter, after: cursor });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  if (state.error) {
    return (
      <Alert tone="danger" title="Could not load the insurance report" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        {state.error.message} ({state.error.code})
      </Alert>
    );
  }
  const rows = [...(state.data?.items ?? []), ...more];
  return (
    <div className={styles.stack}>
      {state.data ? (
        <StatGroup label="Report summary">
          <StatTile label="Policies" value={rows.length} />
          <StatTile label="Under-insured" value={rows.filter((r) => r.underInsured).length} tone="danger" />
          <StatTile label="Expiring within 30 days" value={rows.filter((r) => r.expiringSoon).length} tone="warning" />
        </StatGroup>
      ) : null}
      <DataTable
        label="Insurance report"
        columns={columns}
        rows={rows}
        getRowKey={(r) => r.id}
        loading={state.loading}
        empty={{ title: "No policies in force", description: "No policy of this legal entity covers the report date with these filters." }}
        onRowOpen={(r) => navigate(`/assets/${r.assetId}`)}
      />
      {moreError ? (
        <Alert tone="danger" title="Could not load more policies" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** Insurance report (7.37.14): policies in force with insured vs indexed replacement vs NBV and the under-insured flag. */
export function InsuranceReportPage() {
  const [values, setValues] = useState<ReportValues>(() => ({ legalEntityId: "", asOf: today(), insuranceType: "", expiringWithinDays: "" }));
  const [errors, setErrors] = useState<ReportErrors>({});
  const [filter, setFilter] = useState<InsuranceReportFilter | null>(null);

  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, filter: next } = validateReport(values);
    setErrors(found);
    if (next) setFilter(next);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Insurance report"
        description="Policies in force on the report date. An asset is under-insured when its sum insured is below 90% of the indexed replacement value."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Insurance report filters">
          <FormField id="report-legalEntityId" label="Legal entity" hint="UUID of the legal entity." error={errors.legalEntityId}>
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
          <FormField id="report-asOf" label="Report date" error={errors.asOf}>
            {(control) => <Input {...control} type="date" value={values.asOf} onChange={(e) => setValues((v) => ({ ...v, asOf: e.target.value }))} invalid={Boolean(errors.asOf)} />}
          </FormField>
          <FormField id="report-insuranceType" label="Insurance type">
            {(control) => (
              <Select {...control} value={values.insuranceType} onChange={(e) => setValues((v) => ({ ...v, insuranceType: e.target.value as ReportValues["insuranceType"] }))}>
                <option value="">All types</option>
                {INSURANCE_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="report-expiringWithinDays" label="Expiring within (days)" hint="Leave empty for every policy in force." error={errors.expiringWithinDays}>
            {(control) => (
              <Input
                {...control}
                type="number"
                value={values.expiringWithinDays}
                onChange={(e) => setValues((v) => ({ ...v, expiringWithinDays: e.target.value }))}
                invalid={Boolean(errors.expiringWithinDays)}
              />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Run report
            </Button>
          </div>
        </form>
      </Card>
      {filter ? <ReportResults filter={filter} /> : <EmptyState title="Choose a legal entity" description="Run the report to list the policies in force on the date." />}
    </div>
  );
}
