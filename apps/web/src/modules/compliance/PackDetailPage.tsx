import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { checkInstall, describeProblem, getPack, validateTaxId, type Problem, type StatutoryReport } from "./api/client";
import { sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const COUNTRY = /^[A-Za-z]{2}$/;
const COUNT = /^\d+$/;

const reportColumns: DataTableColumn<StatutoryReport>[] = [
  { key: "code", header: "Report", mono: true },
  { key: "mandate", header: "Mandate" },
  { key: "format", header: "Format", mono: true },
  { key: "requiredFields", header: "Required fields", render: (r) => r.requiredFields.length },
];

type Outcome = { tone: "success"; text: string } | { tone: "danger"; problem: Problem } | null;

/** Always-mounted live regions: a failure is announced as an alert, a success as a status. */
function OutcomeAlerts({ outcome }: { outcome: Outcome }) {
  return (
    <>
      <Alert tone="danger" live="alert">
        {outcome?.tone === "danger" ? <ProblemMessage problem={outcome.problem} /> : null}
      </Alert>
      <Alert tone="success" live="status">
        {outcome?.tone === "success" ? outcome.text : null}
      </Alert>
    </>
  );
}

/** One country pack: its contents, the install check and the pack's tax-id validator (FR-48.1, FR-48.2). */
export function PackDetailPage() {
  const { code = "" } = useParams();
  const state = useLoad((signal) => getPack(code, signal), code);
  const [install, setInstall] = useState({ entityCountry: "", primaryInstalled: "0", calendarOk: true });
  const [installErrors, setInstallErrors] = useState<Record<string, string>>({});
  const [installOutcome, setInstallOutcome] = useState<Outcome>(null);
  const [taxId, setTaxId] = useState("");
  const [taxOutcome, setTaxOutcome] = useState<Outcome>(null);
  const [busy, setBusy] = useState<"install" | "tax" | null>(null);

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Country pack" />
        <Alert tone="danger" title="Could not load the country pack" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link to="/compliance/packs">Back to country packs</Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Country pack" />
        <span className="visually-hidden">Loading the country pack…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const pack = state.data;

  async function submitInstall(event: FormEvent) {
    event.preventDefault();
    const errors: Record<string, string> = {};
    if (!COUNTRY.test(install.entityCountry.trim())) errors.entityCountry = "Enter the legal entity's two-letter country code.";
    if (!COUNT.test(install.primaryInstalled)) errors.primaryInstalled = "Enter 0 or more.";
    setInstallErrors(errors);
    setInstallOutcome(null);
    if (Object.keys(errors).length > 0) return;
    setBusy("install");
    try {
      const res = await checkInstall(pack.code, {
        entityCountry: install.entityCountry.trim().toUpperCase(),
        primaryInstalled: Number(install.primaryInstalled),
        calendarOpenPeriodsOk: install.calendarOk,
      });
      setInstallOutcome({ tone: "success", text: `Pack ${res.pack} ${res.version} can be installed on this legal entity.` });
    } catch (error) {
      setInstallOutcome({ tone: "danger", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  async function submitTaxId(event: FormEvent) {
    event.preventDefault();
    setTaxOutcome(null);
    if (!taxId.trim()) {
      setTaxOutcome({ tone: "danger", problem: { status: 0, code: "COM-0002", message: "Enter a tax id to check.", fields: {} } });
      return;
    }
    setBusy("tax");
    try {
      await validateTaxId(pack.taxIdScheme, taxId.trim());
      setTaxOutcome({ tone: "success", text: `${taxId.trim()} is a valid ${pack.taxIdScheme}.` });
    } catch (error) {
      setTaxOutcome({ tone: "danger", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  const facts: [string, ReactNode][] = [
    ["Country", `${pack.name} (${pack.country})`],
    ["Version", <span className={styles.mono}>{pack.version}</span>],
    ["Currency", <span className={styles.mono}>{pack.currency}</span>],
    ["Locale", <span className={styles.mono}>{pack.locale}</span>],
    ["Tax regime", pack.taxRegime],
    ["Tax id scheme", <span className={styles.mono}>{pack.taxIdScheme}</span>],
    ["Fiscal year starts", new Date(Date.UTC(2026, pack.fiscalYearStartMonth - 1, 1)).toLocaleString("en", { month: "long", timeZone: "UTC" })],
    ["Holiday calendar", <span className={styles.mono}>{pack.holidayRegion}</span>],
    ["Bank formats", pack.bankFormats.join(", ")],
    ["Document layouts", pack.documentLayouts.join(", ")],
  ];
  const retention = Object.entries(pack.retentionMinYears).sort(([a], [b]) => a.localeCompare(b));

  return (
    <div className={styles.stack}>
      <PageHeader title={`${pack.name} pack`} description="Packs are metadata, never code: installing one on a legal entity applies its tax rules, reports, layouts and formats." actions={<Link to="/compliance/packs">Back to country packs</Link>} />
      <Card title="Contents">
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
          {retention.map(([recordClass, years]) => (
            <div key={recordClass} className={styles.entry}>
              <dt className={styles.term}>Minimum retention, {sentence(recordClass).toLowerCase()} records</dt>
              <dd className={styles.value}>{years} years</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Statutory reports">
        <DataTable
          label="Statutory reports"
          columns={reportColumns}
          rows={pack.statutoryReports}
          getRowKey={(r) => r.code}
          empty={{ title: "No statutory reports in this pack", description: "Reports arrive with the pack's electronic reporting formats." }}
        />
      </Card>
      <Card title="Install check">
        <form className={styles.form} onSubmit={submitInstall} noValidate aria-label="Install check">
          <FormField id="install-country" label="Legal entity country" hint="ISO 3166 two-letter code, e.g. GB for the UK pack." error={installErrors.entityCountry}>
            {(control) => <Input {...control} className={styles.mono} value={install.entityCountry} onChange={(e) => setInstall({ ...install, entityCountry: e.target.value })} invalid={Boolean(installErrors.entityCountry)} />}
          </FormField>
          <FormField id="install-primary" label="Primary packs installed" hint="A legal entity has exactly one primary pack." error={installErrors.primaryInstalled}>
            {(control) => <Input {...control} inputMode="numeric" value={install.primaryInstalled} onChange={(e) => setInstall({ ...install, primaryInstalled: e.target.value })} invalid={Boolean(installErrors.primaryInstalled)} />}
          </FormField>
          <span className={styles.check}>
            <Input id="install-calendar" type="checkbox" checked={install.calendarOk} onChange={(e) => setInstall({ ...install, calendarOk: e.target.checked })} />
            <label htmlFor="install-calendar">Fiscal calendar open periods are compatible</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "install"} disabled={busy === "tax"}>
              Check install
            </Button>
          </div>
        </form>
        <OutcomeAlerts outcome={installOutcome} />
      </Card>
      <Card title="Tax id check">
        <form className={styles.toolbar} onSubmit={submitTaxId} noValidate aria-label="Tax id check">
          <FormField id="tax-id" label={`Tax id (${pack.taxIdScheme})`}>
            {(control) => <Input {...control} className={styles.mono} value={taxId} onChange={(e) => setTaxId(e.target.value)} />}
          </FormField>
          <Button type="submit" loading={busy === "tax"} disabled={busy === "install"}>
            Check tax id
          </Button>
        </form>
        <OutcomeAlerts outcome={taxOutcome} />
      </Card>
    </div>
  );
}
