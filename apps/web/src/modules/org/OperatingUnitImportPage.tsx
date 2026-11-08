import { useId, useMemo, useState, type ChangeEvent } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  PageHeader,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem } from "./api/client";
import {
  importOperatingUnits,
  MAX_IMPORT_ROWS,
  parseOperatingUnitCsv,
  type UnitImportReport,
  type UnitImportRowReport,
} from "./api/unitImport";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";

const example =
  "type,code,name,legalEntity\ndepartment,D200,Assembly,USMF\ncost_center,CC100,Plant overhead,\n";

const statusLabel: Record<UnitImportRowReport["status"], string> = {
  created: "To create",
  unchanged: "Already present",
  failed: "Failed",
};

const columns: DataTableColumn<UnitImportRowReport>[] = [
  { key: "line", header: "Row", align: "end" },
  { key: "type", header: "Type" },
  { key: "code", header: "Code", mono: true },
  {
    key: "status",
    header: "Result",
    render: (row) => (
      <Badge
        tone={
          row.status === "failed"
            ? "danger"
            : row.status === "created"
              ? "success"
              : "neutral"
        }
      >
        {statusLabel[row.status]}
      </Badge>
    ),
  },
  {
    key: "errors",
    header: "Problems",
    render: (row) =>
      row.errors
        ? Object.entries(row.errors)
            .map(([field, message]) => `${field}: ${message}`)
            .join("; ")
        : "",
  },
];

export function OperatingUnitImportPage() {
  const { canWriteOperatingUnit } = useLegalEntity();
  const fileId = useId();
  const [text, setText] = useState("");
  const [running, setRunning] = useState<"check" | "import" | null>(null);
  const [report, setReport] = useState<UnitImportReport | null>(null);
  const [failure, setFailure] = useState<{
    message: string;
    code: string;
  } | null>(null);
  const parsed = useMemo(
    () => (text.trim() === "" ? null : parseOperatingUnitCsv(text)),
    [text],
  );
  const blocked = parsed === null || parsed.problems.length > 0;

  const run = async (dryRun: boolean) => {
    if (!parsed || blocked) {
      return;
    }
    setRunning(dryRun ? "check" : "import");
    setFailure(null);
    setReport(null);
    try {
      setReport(await importOperatingUnits(parsed.rows, dryRun));
    } catch (error) {
      setFailure(describeProblem(error));
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

  return (
    <>
      <PageHeader
        title="Import operating units"
        description="Load operating units from a CSV file. Rows that match an existing unit by type and code are left alone; if any row fails, nothing is written."
        actions={<Link to="/org/operating-units">Back to operating units</Link>}
      />
      {!canWriteOperatingUnit ? (
        <Alert tone="info" title="Read only" live="status">
          Importing operating units needs the org.operating-unit.write duty.
        </Alert>
      ) : (
        <Card title="CSV file">
          <div className={styles.form}>
            <FormField
              id={fileId}
              label="File"
              hint={`Columns: type (department, cost_center, business_unit or value_stream), code, name and an optional legalEntity code. Up to ${MAX_IMPORT_ROWS} rows.`}
            >
              {(control) => (
                <input
                  {...control}
                  type="file"
                  accept=".csv,text/csv"
                  onChange={chooseFile}
                />
              )}
            </FormField>
            <FormField
              id={`${fileId}-text`}
              label="CSV text"
              hint="Paste or edit the rows here."
            >
              {(control) => (
                <Textarea
                  {...control}
                  mono
                  className={styles.packageText}
                  placeholder={example}
                  value={text}
                  onChange={(event) => setText(event.target.value)}
                />
              )}
            </FormField>
          </div>
          {parsed && parsed.problems.length > 0 ? (
            <Alert tone="danger" title="Could not read the file" live="alert">
              <ul className={styles.problemList}>
                {parsed.problems.map((problem) => (
                  <li key={problem}>{problem}</li>
                ))}
              </ul>
            </Alert>
          ) : null}
          <div className={styles.packageActions}>
            <Button
              variant="secondary"
              disabled={blocked}
              loading={running === "check"}
              onClick={() => run(true)}
            >
              Check file
            </Button>
            <Button
              variant="primary"
              disabled={blocked}
              loading={running === "import"}
              onClick={() => run(false)}
            >
              Import operating units
            </Button>
            {parsed && !blocked ? (
              <span role="status">{parsed.rows.length} rows read.</span>
            ) : null}
          </div>
        </Card>
      )}
      {failure ? (
        <Alert tone="danger" title="The request failed" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {report ? (
        <Card title={report.dryRun ? "Check result" : "Import result"}>
          <Alert
            tone={
              report.failed > 0 ? "danger" : report.applied ? "success" : "info"
            }
            live="status"
          >
            {report.failed > 0
              ? `${report.failed} rows failed, so nothing was written. Fix them and run the file again.`
              : report.dryRun
                ? `Nothing was written. An import would create ${report.created} operating units and find ${report.unchanged} already present.`
                : `Created ${report.created} operating units; ${report.unchanged} were already present.`}
          </Alert>
          <DataTable
            label="Import rows"
            columns={columns}
            rows={report.rows}
            getRowKey={(row) => String(row.line)}
            empty={{ title: "The file holds no rows" }}
          />
        </Card>
      ) : !failure && canWriteOperatingUnit ? (
        <EmptyState
          compact
          title="No import run yet"
          description="Check a file to see what an import would create before you write it."
        />
      ) : null}
    </>
  );
}
