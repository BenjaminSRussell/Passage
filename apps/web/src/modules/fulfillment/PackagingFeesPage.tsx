import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listFeeRates, packagingReport, saveFeeRate, type FeeRate, type PackagingReportRow, type Problem } from "./api/client";
import { COUNTRY, CURRENCY, DECIMAL, formatDate, formatMoney, MATERIAL_CODE, MONTH, today, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const rateColumns: DataTableColumn<FeeRate>[] = [
  { key: "countryCode", header: "Country" },
  { key: "materialCode", header: "Material", mono: true },
  { key: "ratePerKg", header: "Rate per kg", align: "end", render: (row) => `${row.ratePerKg} ${row.currency}` },
  { key: "validFrom", header: "Valid from", render: (row) => formatDate(row.validFrom) },
];

const reportColumns: DataTableColumn<PackagingReportRow>[] = [
  { key: "materialCode", header: "Material", mono: true },
  { key: "weightKg", header: "Weight kg", align: "end" },
  { key: "feeAmount", header: "Fee", align: "end", render: (row) => formatMoney(row.feeAmount, row.currency) },
];

interface RateValues {
  countryCode: string;
  materialCode: string;
  currency: string;
  ratePerKg: string;
  validFrom: string;
}

const blankRate = (): RateValues => ({ countryCode: "", materialCode: "", currency: "EUR", ratePerKg: "", validFrom: today() });

/** The packaging fee report of one country and month (section 5.28.14, AC-28.12). */
function PackagingReport() {
  const [values, setValues] = useState({ countryCode: "", period: today().slice(0, 7), legalEntityId: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [query, setQuery] = useState<typeof values | null>(null);
  const report = useLoad<PackagingReportRow[] | null>(
    (signal) => (query ? packagingReport({ countryCode: query.countryCode, period: query.period, legalEntityId: query.legalEntityId || undefined }, signal) : Promise.resolve(null)),
    query ? `${query.countryCode}/${query.period}/${query.legalEntityId}` : "",
  );

  function run(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!COUNTRY.test(values.countryCode.trim())) found.countryCode = "Enter a 2-letter country code, such as DE.";
    if (!MONTH.test(values.period.trim())) found.period = "Enter the month as YYYY-MM.";
    if (values.legalEntityId.trim() !== "" && !UUID.test(values.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID), or leave it empty.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setQuery({ countryCode: values.countryCode.trim(), period: values.period.trim(), legalEntityId: values.legalEntityId.trim() });
  }

  return (
    <Card title="Packaging report">
      <form className={styles.form} onSubmit={run} noValidate>
        <FormField id="report-country" label="Country" error={errors.countryCode}>
          {(control) => <Input {...control} maxLength={2} value={values.countryCode} invalid={Boolean(errors.countryCode)} onChange={(event) => setValues({ ...values, countryCode: event.target.value.toUpperCase() })} />}
        </FormField>
        <FormField id="report-period" label="Month" hint="YYYY-MM" error={errors.period}>
          {(control) => <Input {...control} maxLength={7} value={values.period} invalid={Boolean(errors.period)} onChange={(event) => setValues({ ...values, period: event.target.value })} />}
        </FormField>
        <FormField id="report-le" label="Legal entity" error={errors.legalEntityId} optional>
          {(control) => <EntityLookup entity="legalEntity" prefill={false} {...control} value={values.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(event) => setValues({ ...values, legalEntityId: event.target.value })} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary">
            Show report
          </Button>
        </div>
      </form>
      {query ? (
        report.error ? (
          <Alert tone="danger" title="Could not load the report" live="alert" action={<Button size="sm" onClick={report.reload}>Try again</Button>}>
            <ProblemMessage problem={report.error} />
          </Alert>
        ) : (
          <DataTable
            label={`Packaging report ${query.countryCode} ${query.period}`}
            columns={reportColumns}
            rows={report.data ?? []}
            getRowKey={(row) => `${row.materialCode}-${row.currency}`}
            loading={report.loading}
            empty={{ title: "Nothing shipped", description: "No packing slip with packaging fees was posted for this country and month." }}
          />
        )
      ) : null}
    </Card>
  );
}

/** Packaging fee rates per country and material (FR-28.14) and the period report. */
export function PackagingFeesPage() {
  const [country, setCountry] = useState("");
  const filter = COUNTRY.test(country) ? country : "";
  const state = useLoad((signal) => listFeeRates({ countryCode: filter }, signal), filter);
  const [values, setValues] = useState<RateValues>(blankRate);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function set(patch: Partial<RateValues>) {
    setValues({ ...values, ...patch });
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!COUNTRY.test(values.countryCode.trim())) found.countryCode = "Enter a 2-letter country code, such as DE.";
    if (!MATERIAL_CODE.test(values.materialCode.trim())) found.materialCode = "Enter a material code of up to 32 letters or digits, such as CARDBOARD.";
    if (!CURRENCY.test(values.currency.trim())) found.currency = "Enter a 3-letter currency code, such as EUR.";
    if (!DECIMAL.test(values.ratePerKg.trim())) found.ratePerKg = "Enter the fee per kg, 0 or more, with at most 6 decimals.";
    if (!/^\d{4}-\d{2}-\d{2}$/.test(values.validFrom)) found.validFrom = "Enter the date the rate applies from.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await saveFeeRate(
        { countryCode: values.countryCode.trim(), materialCode: values.materialCode.trim(), currency: values.currency.trim(), ratePerKg: values.ratePerKg.trim(), validFrom: values.validFrom },
        key.current(),
      );
      key.renew();
      setValues(blankRate());
      setNotice(`Saved ${saved.materialCode} in ${saved.countryCode}: ${saved.ratePerKg} ${saved.currency} per kg from ${saved.validFrom}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Packaging fees"
        description="Country fee rates per kg of packaging material. A slip uses the latest rate valid on its posting date; a shipped material without a rate stops the post (FUL-0014)."
      />
      <div className={styles.toolbar}>
        <FormField id="rates-country" label="Country" hint="Show one country's rates." optional>
          {(control) => <Input {...control} maxLength={2} value={country} onChange={(event) => setCountry(event.target.value.toUpperCase())} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load fee rates" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Packaging fee rates"
          columns={rateColumns}
          rows={state.data ?? []}
          getRowKey={(row) => `${row.countryCode}-${row.materialCode}-${row.validFrom}`}
          loading={state.loading}
          empty={{ title: "No fee rates yet", description: "Load the country rate table (for example DE LUCID or FR CITEO rates) before posting slips to that country." }}
        />
      )}
      <Card title="Add a fee rate">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="rate-country" label="Country" error={errors.countryCode}>
            {(control) => <Input {...control} maxLength={2} value={values.countryCode} invalid={Boolean(errors.countryCode)} onChange={(event) => set({ countryCode: event.target.value.toUpperCase() })} />}
          </FormField>
          <FormField id="rate-material" label="Material code" error={errors.materialCode}>
            {(control) => <Input {...control} maxLength={32} value={values.materialCode} invalid={Boolean(errors.materialCode)} onChange={(event) => set({ materialCode: event.target.value.toUpperCase() })} />}
          </FormField>
          <FormField id="rate-currency" label="Currency" error={errors.currency}>
            {(control) => <Input {...control} maxLength={3} value={values.currency} invalid={Boolean(errors.currency)} onChange={(event) => set({ currency: event.target.value.toUpperCase() })} />}
          </FormField>
          <FormField id="rate-rate" label="Rate per kg" error={errors.ratePerKg}>
            {(control) => <Input {...control} inputMode="decimal" value={values.ratePerKg} invalid={Boolean(errors.ratePerKg)} onChange={(event) => set({ ratePerKg: event.target.value })} />}
          </FormField>
          <FormField id="rate-from" label="Valid from" error={errors.validFrom}>
            {(control) => <Input {...control} type="date" value={values.validFrom} invalid={Boolean(errors.validFrom)} onChange={(event) => set({ validFrom: event.target.value })} />}
          </FormField>
          <ProblemAlert title="Could not save the fee rate" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save fee rate
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </Card>
      <PackagingReport />
    </div>
  );
}
