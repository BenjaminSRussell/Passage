import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  DEFINITION_KINDS,
  describeProblem,
  exportDefinition,
  getDefinition,
  publishDefinition,
  ReportingApiError,
  retireDefinition,
  saveDraft,
  validateDefinition,
  type Definition,
  type DefinitionColumn,
  type DefinitionKind,
  type DefinitionRow,
  type Issue,
  type Problem,
} from "./api/client";
import {
  DEFINITION_CODE,
  KIND_LABELS,
} from "./format";
import { ColumnsEditor, RowsEditor } from "./PartEditors";
import { PartsPicker } from "./PartsPicker";
import styles from "./pages.module.css";

function notFoundProblem(code: string) {
  return new ReportingApiError(404, "REPORT-0020", `Definition ${code} has no draft and no published version.`);
}

export function blankDefinition(): Definition {
  return {
    schemaVersion: "1",
    code: "",
    name: "",
    version: 0,
    status: "draft",
    kind: "custom",
    includeAdjustmentPeriods: false,
    rows: [{ code: "R1", kind: "account_range", label: "", accountFrom: "", accountTo: "", normalBalance: "debit", format: {} }],
    columns: [{ code: "C1", label: "Period", periodExpr: "P" }],
  };
}

/** Drops the fields a row kind does not use, so the service sees a clean row. */
export function cleanRow(row: DefinitionRow): DefinitionRow {
  const out: DefinitionRow = { code: row.code.trim(), kind: row.kind, label: row.label, normalBalance: row.normalBalance, format: row.format ?? {} };
  if (row.cashFlowTag) out.cashFlowTag = row.cashFlowTag;
  if (row.kind === "account_range") {
    out.accountFrom = (row.accountFrom ?? "").trim();
    out.accountTo = (row.accountTo ?? "").trim();
  }
  if ((row.kind === "account_range" || row.kind === "dimension_filter") && row.dimensionFilter && Object.keys(row.dimensionFilter).length > 0) {
    out.dimensionFilter = row.dimensionFilter;
  }
  if (row.kind === "total") out.references = row.references ?? [];
  if (row.kind === "calculation") out.expression = row.expression ?? "";
  return out;
}

export function cleanColumn(col: DefinitionColumn): DefinitionColumn {
  const out: DefinitionColumn = { code: col.code.trim(), label: col.label, periodExpr: col.periodExpr.trim() };
  if (out.periodExpr === "CALC") out.calcExpression = col.calcExpression ?? "";
  else {
    if (col.currencyType) out.currencyType = col.currencyType;
    if (col.dimensionFilter && Object.keys(col.dimensionFilter).length > 0) out.dimensionFilter = col.dimensionFilter;
    if (col.balanceSide) out.balanceSide = col.balanceSide;
  }
  return out;
}

function cleanDefinition(def: Definition): Definition {
  return { ...def, code: def.code.trim(), rows: def.rows.map(cleanRow), columns: def.columns.map(cleanColumn) };
}

interface Loaded {
  def: Definition;
  etag: string | null;
  /** Newest non-draft version (0 when never published). */
  basedOn: number;
  /** Status of that version: published (runnable) or retired (BR-15.8). */
  basedOnStatus: "published" | "retired" | null;
}

/**
 * Report designer (FR-15.2, FR-15.3, BR-15.8): edits the draft of a definition.
 * Without a draft it starts a new one from the newest published version.
 */
export function DefinitionEditorPage() {
  // Viewers of statements can open a definition; only designers read the saved parts.
  const canUseParts = holdsDuty(usePrincipalDuties(), "reporting.definition.maintain");
  const { code: routeCode } = useParams();
  const isNew = routeCode === undefined;
  const navigate = useNavigate();
  const [loaded, setLoaded] = useState<Loaded | null>(isNew ? { def: blankDefinition(), etag: null, basedOn: 0, basedOnStatus: null } : null);
  const [loadError, setLoadError] = useState<Problem | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [def, setDef] = useState<Definition>(blankDefinition());
  const [etag, setEtag] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [issues, setIssues] = useState<Issue[] | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<"save" | "validate" | "publish" | "export" | "retire" | null>(null);
  const [confirmRetire, setConfirmRetire] = useState(false);

  useEffect(() => {
    if (isNew) {
      setDef(blankDefinition());
      return;
    }
    const controller = new AbortController();
    const code = routeCode ?? "";
    setLoaded(null);
    setLoadError(null);
    const notFound = (error: unknown) => {
      if (describeProblem(error).status !== 404) throw error;
      return null;
    };
    (async (): Promise<Loaded> => {
      const [draft, latest] = await Promise.all([
        getDefinition(code, "draft", controller.signal).catch(notFound),
        getDefinition(code, "latest", controller.signal).catch(notFound),
      ]);
      const basedOn = latest?.definition.version ?? 0;
      const basedOnStatus = latest ? (latest.definition.status === "retired" ? "retired" : "published") : null;
      if (draft) return { def: draft.definition, etag: draft.etag, basedOn, basedOnStatus };
      if (latest) return { def: { ...latest.definition, version: 0, status: "draft" }, etag: null, basedOn, basedOnStatus };
      throw notFoundProblem(code);
    })().then(
      (result) => {
        setLoaded(result);
        setDef(result.def);
        setEtag(result.etag);
        setDirty(false);
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setLoadError(describeProblem(error));
      },
    );
    return () => controller.abort();
  }, [routeCode, isNew, attempt]);

  function change(patch: Partial<Definition>) {
    setDef((prev) => ({ ...prev, ...patch }));
    setDirty(true);
    setNotice(null);
  }

  async function validate() {
    setBusy("validate");
    setFailure(null);
    try {
      const result = await validateDefinition(cleanDefinition(def));
      setIssues(result.issues);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function save(event?: FormEvent) {
    event?.preventDefault();
    if (!DEFINITION_CODE.test(def.code.trim())) {
      setIssues([{ field: "code", code: "COM-0001", detail: "Use 2 to 30 upper-case letters, digits or hyphens, starting with a letter." }]);
      return;
    }
    setBusy("save");
    setFailure(null);
    setIssues(null);
    try {
      const saved = await saveDraft(cleanDefinition(def), etag);
      setEtag(saved.etag);
      setDef(saved.definition);
      setDirty(false);
      setNotice("Draft saved.");
      if (isNew) navigate(`/finreport/definitions/${encodeURIComponent(saved.definition.code)}`, { replace: true });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function publish() {
    setBusy("publish");
    setFailure(null);
    try {
      const published = await publishDefinition(def.code.trim());
      setLoaded({ def: { ...published, version: 0, status: "draft" }, etag: null, basedOn: published.version, basedOnStatus: "published" });
      setDef({ ...published, version: 0, status: "draft" });
      setEtag(null);
      setDirty(false);
      setNotice(`Published version ${published.version}. Runs can use it now.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function retire() {
    setBusy("retire");
    setFailure(null);
    try {
      const result = await retireDefinition(def.code.trim());
      setLoaded((prev) => (prev ? { ...prev, basedOnStatus: "retired" } : prev));
      setConfirmRetire(false);
      setNotice(
        `Retired ${result.code}. New runs can no longer use it; earlier runs stay readable.${hasDraft ? " Publishing the open draft makes it runnable again." : ""}`,
      );
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function download() {
    setBusy("export");
    setFailure(null);
    try {
      const exported = await exportDefinition(def.code);
      const blob = new Blob([JSON.stringify(exported, null, 2)], { type: "application/json" });
      if (typeof URL.createObjectURL === "function") {
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = `${exported.code}-v${exported.version}.json`;
        a.click();
        URL.revokeObjectURL(url);
      }
      setNotice(`Exported ${exported.code} version ${exported.version}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const title = isNew ? "New report definition" : `Report definition ${routeCode}`;
  if (loadError) {
    const missing = loadError.status === 404;
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert
          tone="danger"
          title={missing ? "This definition does not exist" : "This definition could not be loaded"}
          live="alert"
          action={missing ? undefined : <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Try again</Button>}
        >
          {loadError.message} ({loadError.code})
        </Alert>
        <Link to="/finreport/definitions">Back to report definitions</Link>
      </div>
    );
  }
  if (!loaded) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.note}>Loading definition…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }

  const hasDraft = etag !== null;
  const published = loaded.basedOn > 0 && loaded.basedOnStatus === "published";
  const retired = loaded.basedOnStatus === "retired";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description={
          hasDraft
            ? `You are editing the draft. Publishing turns it into version ${loaded.basedOn + 1}.`
            : loaded.basedOn > 0
              ? `No draft is open. Saving starts a draft from ${retired ? "retired" : "published"} version ${loaded.basedOn}.`
              : "Saving creates the first draft of this definition."
        }
        meta={
          <>
            {hasDraft ? <Badge tone="info">Draft</Badge> : null}
            {published ? <Badge tone="success">{`v${loaded.basedOn} published`}</Badge> : null}
            {retired ? <Badge tone="warning">{`v${loaded.basedOn} retired`}</Badge> : null}
          </>
        }
        actions={
          published ? (
            <Link className={buttonClassName("secondary")} to={`/finreport/runs/new?definition=${encodeURIComponent(def.code)}`}>
              Run this statement
            </Link>
          ) : undefined
        }
      />
      <form className={styles.stack} onSubmit={save} noValidate>
        <Card title="Definition">
          <div className={styles.form}>
            <FormField id="fr-def-code" label="Definition code" hint={isNew ? "Upper-case letters, digits and hyphens, such as BS-STD." : undefined}>
              {(c) => <Input {...c} className={styles.mono} value={def.code} readOnly={!isNew} onChange={(e) => change({ code: e.target.value.toUpperCase() })} />}
            </FormField>
            <FormField id="fr-def-name" label="Name">
              {(c) => <Input {...c} value={def.name} onChange={(e) => change({ name: e.target.value })} />}
            </FormField>
            <FormField id="fr-def-kind" label="Statement kind" hint="Balance sheets and trial balances are cumulative; income statements leave out closing entries.">
              {(c) => (
                <Select {...c} value={def.kind} onChange={(e) => change({ kind: e.target.value as DefinitionKind })}>
                  {DEFINITION_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {KIND_LABELS[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <span className={styles.check}>
              <Input id="fr-def-adjustment" type="checkbox" checked={def.includeAdjustmentPeriods} onChange={(e) => change({ includeAdjustmentPeriods: e.target.checked })} />
              <label htmlFor="fr-def-adjustment">Include adjustment periods</label>
            </span>
          </div>
        </Card>

        {canUseParts ? <PartsPicker onRows={(rows) => change({ rows })} onColumns={(columns) => change({ columns })} /> : null}

        <RowsEditor rows={def.rows} onChange={(rows) => change({ rows })} />

        <ColumnsEditor columns={def.columns} onChange={(columns) => change({ columns })} />

        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy === "save"}>
            Save draft
          </Button>
          <Button onClick={validate} loading={busy === "validate"}>
            Validate
          </Button>
          <Button onClick={publish} loading={busy === "publish"} disabled={!hasDraft || dirty}>
            Publish
          </Button>
          {loaded.basedOn > 0 ? (
            <Button variant="ghost" onClick={download} loading={busy === "export"}>
              Export JSON
            </Button>
          ) : null}
          {published ? (
            <Button variant="ghost" onClick={() => setConfirmRetire(true)} disabled={busy !== null}>
              Retire
            </Button>
          ) : null}
          {dirty ? <span className={styles.note}>Unsaved changes. Save the draft before publishing.</span> : null}
        </div>
      </form>

      {confirmRetire ? (
        <Alert
          tone="warning"
          title={`Retire ${def.code}?`}
          live="alert"
          action={
            <span className={styles.lineActions}>
              <Button size="sm" variant="primary" onClick={retire} loading={busy === "retire"}>
                Retire definition
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setConfirmRetire(false)}>
                Keep it
              </Button>
            </span>
          }
        >
          Runs and schedules can no longer use any published version. Earlier runs stay readable.
        </Alert>
      ) : null}
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {issues !== null ? (
        issues.length === 0 ? (
          <Alert tone="success" title="The definition is valid" live="status" />
        ) : (
          <Alert tone="warning" title={`${issues.length} problem${issues.length === 1 ? "" : "s"} to fix`} live="alert">
            <ul className={styles.issues}>
              {issues.map((is, i) => (
                <li key={i}>
                  <span className={styles.mono}>{is.field}</span>: {is.detail} ({is.code})
                </li>
              ))}
            </ul>
          </Alert>
        )
      ) : null}
      {failure ? (
        <Alert
          tone="danger"
          title={failure.code === "conflict_version" || failure.code === "precondition_required" ? "Someone else changed this draft" : "That did not work"}
          live="alert"
          action={
            failure.code === "conflict_version" || failure.code === "precondition_required" ? (
              <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
                Reload the draft
              </Button>
            ) : undefined
          }
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </div>
  );
}
