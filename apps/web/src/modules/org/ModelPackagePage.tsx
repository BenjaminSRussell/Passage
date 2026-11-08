import { useId, useState, type ChangeEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { exportPackage, importPackage, PackageError, type ImportReport } from "./api/package";
import { describeProblem } from "./api/client";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";

interface Failure {
  title: string;
  code: string;
  message: string;
  fields: Record<string, string>;
}

interface KindRow {
  kind: string;
  created: number;
  unchanged: number;
}

const kindLabels: Record<string, string> = {
  legalEntities: "Legal entities",
  operatingUnits: "Operating units",
  sites: "Sites",
  warehouses: "Warehouses",
  locations: "Locations",
  hierarchyTypes: "Hierarchy types",
  hierarchyVersions: "Hierarchy versions",
  fiscalCalendars: "Fiscal calendars",
  fiscalYears: "Fiscal years",
  teamTypes: "Team types",
  teams: "Teams",
  units: "Units of measure",
  conversions: "Unit conversions",
  currencyRoundingRules: "Currency rounding rules",
  nameTranslations: "Name translations",
};

const columns: DataTableColumn<KindRow>[] = [
  { key: "kind", header: "Kind", render: (row) => kindLabels[row.kind] ?? row.kind },
  { key: "created", header: "Created", align: "end" },
  { key: "unchanged", header: "Already present", align: "end" },
];

function failureOf(title: string, error: unknown): Failure {
  const problem = describeProblem(error);
  return { title, ...problem, fields: error instanceof PackageError ? error.fields : {} };
}

function download(name: string, text: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  // Revoking in the same task can cancel the download in some browsers.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

export function ModelPackagePage() {
  const { canManagePackage } = useLegalEntity();
  const fileId = useId();
  const [text, setText] = useState("");
  const [exporting, setExporting] = useState(false);
  const [running, setRunning] = useState<"check" | "import" | null>(null);
  const [exported, setExported] = useState<string | null>(null);
  const [report, setReport] = useState<ImportReport | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);

  const runExport = async () => {
    setExporting(true);
    setFailure(null);
    try {
      const pkg = await exportPackage();
      download("org-package.json", JSON.stringify(pkg, null, 2));
      setExported(pkg.exportedAt as string);
    } catch (error) {
      setFailure(failureOf("Could not export the organization model", error));
    } finally {
      setExporting(false);
    }
  };

  const runImport = async (dryRun: boolean) => {
    setFailure(null);
    setReport(null);
    try {
      JSON.parse(text);
    } catch {
      setFailure({ title: "Could not read the package", code: "COM-0001", message: "The text is not valid JSON.", fields: {} });
      return;
    }
    setRunning(dryRun ? "check" : "import");
    try {
      setReport(await importPackage(text, dryRun));
    } catch (error) {
      setFailure(failureOf(dryRun ? "The package failed the check" : "The package was not imported", error));
    } finally {
      setRunning(null);
    }
  };

  const chooseFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (file) {
      setText(await file.text());
      setReport(null);
      setFailure(null);
    }
  };

  const rows: KindRow[] = report ? Object.entries(report.kinds).map(([kind, counts]) => ({ kind, ...counts })) : [];

  return (
    <>
      <PageHeader
        title="Model package"
        description="Export the organization model as a versioned JSON package, or import one. Import creates what is missing, matched by code, and never changes existing records."
      />
      {!canManagePackage ? (
        <Alert tone="info" title="Read only" live="status">
          Exporting and importing the model needs the org.package.manage duty and tenant administration.
        </Alert>
      ) : (
        <>
          <Card title="Export">
            <div className={styles.packageActions}>
              <Button variant="secondary" loading={exporting} onClick={runExport}>
                Export package
              </Button>
              {exported ? <span role="status">Exported {exported}. Addresses, translations, calendar assignments and team members are not included.</span> : null}
            </div>
          </Card>
          <Card title="Import">
            <div className={styles.form}>
              <FormField id={fileId} label="Package file" hint="A JSON file exported from this or another tenant.">
                {(control) => <input {...control} type="file" accept="application/json,.json" onChange={chooseFile} />}
              </FormField>
              <FormField id={`${fileId}-text`} label="Package JSON" hint="Paste or edit the package here.">
                {(control) => <Textarea {...control} mono className={styles.packageText} value={text} onChange={(event) => setText(event.target.value)} />}
              </FormField>
            </div>
            <div className={styles.packageActions}>
              <Button variant="secondary" disabled={text.trim() === ""} loading={running === "check"} onClick={() => runImport(true)}>
                Check package
              </Button>
              <Button variant="primary" disabled={text.trim() === ""} loading={running === "import"} onClick={() => runImport(false)}>
                Import package
              </Button>
            </div>
          </Card>
        </>
      )}
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          {failure.message} ({failure.code})
          {Object.keys(failure.fields).length > 0 ? (
            <ul className={styles.problemList}>
              {Object.entries(failure.fields).map(([path, message]) => (
                <li key={path}>
                  <code>{path}</code>: {message}
                </li>
              ))}
            </ul>
          ) : null}
        </Alert>
      ) : null}
      {report ? (
        <Card title={report.dryRun ? "Check result" : "Import result"}>
          <Alert tone={report.created === 0 ? "info" : "success"} live="status">
            {report.dryRun
              ? `Nothing was written. An import would create ${report.created} records and find ${report.unchanged} already present.`
              : `Created ${report.created} records; ${report.unchanged} were already present.`}
          </Alert>
          <DataTable label="Records by kind" columns={columns} rows={rows} getRowKey={(row) => row.kind} empty={{ title: "The package holds no records" }} />
          {report.differences.length > 0 ? (
            <Alert tone="warning" title="Existing records differ from the package and were left as they are">
              <ul className={styles.problemList}>
                {report.differences.map((d) => (
                  <li key={`${d.kind}:${d.code}:${d.field}`}>
                    {kindLabels[d.kind] ?? d.kind} {d.code}: {d.field}
                  </li>
                ))}
              </ul>
            </Alert>
          ) : null}
          {report.skipped && report.skipped.length > 0 ? (
            <Alert tone="info" title="Left out on purpose">
              <ul className={styles.problemList}>
                {report.skipped.map((s) => (
                  <li key={`${s.kind}:${s.code}`}>
                    {kindLabels[s.kind] ?? s.kind} {s.code}: {s.reason}
                  </li>
                ))}
              </ul>
            </Alert>
          ) : null}
        </Card>
      ) : !failure ? (
        <EmptyState compact title="No import run yet" description="Check a package to see what an import would create before you write it." />
      ) : null}
    </>
  );
}
