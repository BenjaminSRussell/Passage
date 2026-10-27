import { useState, type FormEvent } from "react";
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
  StatGroup,
  StatTile,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  freezeSalesEstimate,
  refreshSalesEstimate,
  type Diagnostic,
  type EstimateVersion,
  type EstimateVersionStatus,
  type ExplosionMode,
  type Problem,
} from "./api/client";
import { amount, dash } from "./format";
import { configuredLine, validateEstimate } from "./SalesEstimatePage";
import { DiagnosticsTable, ExplosionModeField, ItemsField, ProblemAlert, itemsText } from "./shared";
import styles from "./pages.module.css";

interface Values {
  lineId: string;
  configHash: string;
  currency: string;
  items: string;
  itemId: string;
  quantity: string;
  netAmount: string;
  mode: ExplosionMode;
  fallbackToActive: boolean;
}

const initial: Values = {
  lineId: "SO-1001/10",
  configHash: "cfg-1",
  currency: "USD",
  items: itemsText(configuredLine),
  itemId: "CFG-LINE",
  quantity: "1",
  netAmount: "2400.00",
  mode: "Multilevel",
  fallbackToActive: false,
};

type Errors = Record<string, string>;

/** The refresh request: the estimate checks of the sales order estimate page plus the line and its configuration. */
export function validateRefresh(values: Values, versions: EstimateVersion[]) {
  const { errors, request } = validateEstimate(values);
  const lineId = values.lineId.trim();
  const configHash = values.configHash.trim();
  if (lineId === "") errors.lineId = "Enter the sales order line.";
  if (configHash === "") errors.configHash = "Enter the configuration version or hash of the line.";
  if (!request || Object.keys(errors).length > 0) return { errors };
  return { errors, request: { ...request, lineId, configHash, versions } };
}

const statusTone: Record<EstimateVersionStatus, BadgeTone> = { Pending: "warning", Frozen: "success", Superseded: "neutral" };

/** The version the line is on: the highest that is not Superseded. */
export function currentVersion(versions: EstimateVersion[]): EstimateVersion | undefined {
  return [...versions].sort((a, b) => b.version - a.version).find((v) => v.status !== "Superseded");
}

const failed = (v: EstimateVersion) => v.estimate.status === "Error";

const columns: DataTableColumn<EstimateVersion>[] = [
  { key: "version", header: "Version", align: "end", render: (v) => String(v.version) },
  { key: "configHash", header: "Configuration", mono: true },
  { key: "status", header: "Status", render: (v) => <Badge tone={statusTone[v.status]}>{v.status}</Badge> },
  { key: "cost", header: "Estimated cost", align: "end", render: (v) => (failed(v) ? "Not costed" : amount(v.estimate.cost)) },
  { key: "margin", header: "Margin", align: "end", render: (v) => (failed(v) ? dash : amount(v.estimate.margin)) },
  { key: "marginPct", header: "Margin %", align: "end", render: (v) => (failed(v) ? dash : amount(v.estimate.marginPct)) },
];

/**
 * The estimate versions of one sales order line (FR-39.16, BR-39.14): recompute
 * on a configuration change, then freeze the current version at confirmation.
 */
export function SalesVersionsPage() {
  const [values, setValues] = useState<Values>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<"refresh" | "freeze" | null>(null);
  const [versions, setVersions] = useState<EstimateVersion[]>([]);
  const [diagnostics, setDiagnostics] = useState<Diagnostic[]>([]);
  const [notice, setNotice] = useState<string | null>(null);

  async function refresh(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateRefresh(values, versions);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (!request) return;
    setBusy("refresh");
    try {
      const res = await refreshSalesEstimate(request);
      setVersions(res.versions);
      setDiagnostics(res.diagnostics ?? []);
      setNotice(
        res.changed
          ? `Version ${res.current.version} is the line's estimate for configuration ${res.current.configHash}.`
          : `The configuration is unchanged, so version ${res.current.version} stays as it is.`,
      );
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure({ title: "The line was not costed", problem });
    } finally {
      setBusy(null);
    }
  }

  async function confirm() {
    setFailure(null);
    setNotice(null);
    setBusy("freeze");
    try {
      const res = await freezeSalesEstimate({ lineId: values.lineId.trim(), versions });
      setVersions(res.versions);
      setDiagnostics([]);
      setNotice(`Version ${res.confirmed.version} is frozen for the confirmed line.`);
    } catch (err) {
      setFailure({ title: "The line's estimate was not frozen", problem: describeProblem(err) });
    } finally {
      setBusy(null);
    }
  }

  const current = currentVersion(versions);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Sales order estimate versions"
        description="Each configuration of a sales order line gets its own estimate version. Refreshing with the same configuration keeps the current version; a new configuration supersedes it. Confirming the line freezes the current version. Versions are not stored yet, so the history lasts for the session."
      />
      <Card title="Sales order line" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void refresh(e)} noValidate>
          <FormField id="versions-line" label="Sales order line" error={errors.lineId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.lineId}
                onChange={(e) => {
                  // The history belongs to one line.
                  setVersions([]);
                  setDiagnostics([]);
                  setNotice(null);
                  setValues({ ...values, lineId: e.target.value });
                }}
                invalid={Boolean(errors.lineId)}
              />
            )}
          </FormField>
          <FormField
            id="versions-config"
            label="Configuration"
            hint="The configuration version or hash from the configurator. Change it to record a configuration change."
            error={errors.configHash}
          >
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.configHash}
                onChange={(e) => setValues({ ...values, configHash: e.target.value })}
                invalid={Boolean(errors.configHash)}
              />
            )}
          </FormField>
          <ItemsField
            id="versions-items"
            label="Order structure"
            value={values.items}
            error={errors.items}
            rows={8}
            onChange={(items) => setValues({ ...values, items })}
          />
          <FormField id="versions-item" label="Line item" error={errors.itemId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.itemId}
                onChange={(e) => setValues({ ...values, itemId: e.target.value })}
                invalid={Boolean(errors.itemId)}
              />
            )}
          </FormField>
          <FormField id="versions-quantity" label="Order quantity" error={errors.quantity}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.quantity}
                onChange={(e) => setValues({ ...values, quantity: e.target.value })}
                invalid={Boolean(errors.quantity)}
              />
            )}
          </FormField>
          <FormField id="versions-net" label="Net line amount" error={errors.netAmount}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.netAmount}
                onChange={(e) => setValues({ ...values, netAmount: e.target.value })}
                invalid={Boolean(errors.netAmount)}
              />
            )}
          </FormField>
          <FormField id="versions-currency" label="Currency" optional error={errors.currency}>
            {(control) => (
              <Input
                {...control}
                value={values.currency}
                onChange={(e) => setValues({ ...values, currency: e.target.value })}
                invalid={Boolean(errors.currency)}
              />
            )}
          </FormField>
          <ExplosionModeField id="versions-mode" value={values.mode} onChange={(mode) => setValues({ ...values, mode })} />
          {failure ? <ProblemAlert title={failure.title} problem={failure.problem} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "refresh"} disabled={busy === "freeze"}>
              Refresh the estimate
            </Button>
            <Button
              variant="secondary"
              loading={busy === "freeze"}
              disabled={busy === "refresh" || current === undefined}
              onClick={() => void confirm()}
            >
              Confirm the line
            </Button>
          </div>
        </form>
      </Card>
      {notice ? (
        <Alert tone="info" title="Estimate versions" live="status">
          {notice}
        </Alert>
      ) : null}
      {current ? (
        <StatGroup label="Current estimate">
          <StatTile label="Version" value={String(current.version)} />
          <StatTile label="Status" value={current.status} />
          <StatTile label="Estimated cost" value={failed(current) ? dash : amount(current.estimate.cost)} />
          <StatTile label="Margin" value={failed(current) ? dash : amount(current.estimate.margin)} />
        </StatGroup>
      ) : null}
      {diagnostics.length > 0 ? <DiagnosticsTable label="Refresh diagnostics" diagnostics={diagnostics} /> : null}
      {current && failed(current) && (current.estimate.diagnostics ?? []).length > 0 ? (
        <DiagnosticsTable label={`Diagnostics of version ${current.version}`} diagnostics={current.estimate.diagnostics ?? []} />
      ) : null}
      {busy === "refresh" && versions.length === 0 ? (
        <DataTable label="Estimate versions" columns={columns} rows={[]} getRowKey={(v) => String(v.version)} loading />
      ) : versions.length === 0 ? (
        <Card>
          <EmptyState
            icon="receipt"
            headingLevel={3}
            title="No estimate versions yet"
            description="Refresh the estimate to cost the line for its configuration. Each configuration change adds a version."
          />
        </Card>
      ) : (
        <DataTable label="Estimate versions" columns={columns} rows={[...versions].reverse()} getRowKey={(v) => String(v.version)} />
      )}
    </div>
  );
}
