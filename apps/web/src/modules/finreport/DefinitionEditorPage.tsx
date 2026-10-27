import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  CURRENCY_TYPES,
  DEFINITION_KINDS,
  describeProblem,
  exportDefinition,
  getDefinition,
  publishDefinition,
  ReportingApiError,
  retireDefinition,
  ROW_KINDS,
  saveDraft,
  validateDefinition,
  type CurrencyType,
  type Definition,
  type DefinitionColumn,
  type DefinitionKind,
  type DefinitionRow,
  type Issue,
  type Problem,
  type RowKind,
} from "./api/client";
import {
  CURRENCY_TYPE_LABELS,
  DEFINITION_CODE,
  formatDimensionFilter,
  KIND_LABELS,
  parseDimensionFilter,
  parseReferences,
  ROW_KIND_LABELS,
} from "./format";
import styles from "./pages.module.css";

const CASH_FLOW_TAGS = ["", "operating_adjustment", "working_capital", "investing", "financing", "cash"] as const;
const CASH_FLOW_LABELS: Record<(typeof CASH_FLOW_TAGS)[number], string> = {
  "": "None",
  operating_adjustment: "Operating adjustment",
  working_capital: "Working capital",
  investing: "Investing",
  financing: "Financing",
  cash: "Cash",
};

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
  else if (col.currencyType) out.currencyType = col.currencyType;
  return out;
}

function cleanDefinition(def: Definition): Definition {
  return { ...def, code: def.code.trim(), rows: def.rows.map(cleanRow), columns: def.columns.map(cleanColumn) };
}

function move<T>(list: T[], from: number, to: number): T[] {
  if (to < 0 || to >= list.length) return list;
  const next = list.slice();
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next;
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
  function changeRow(i: number, patch: Partial<DefinitionRow>) {
    change({ rows: def.rows.map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  }
  function changeColumn(i: number, patch: Partial<DefinitionColumn>) {
    change({ columns: def.columns.map((c, j) => (j === i ? { ...c, ...patch } : c)) });
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

        <Card title={`Rows (${def.rows.length})`} actions={<Button size="sm" onClick={() => change({ rows: [...def.rows, { code: `R${def.rows.length + 1}`, kind: "account_range", label: "", normalBalance: "debit", format: {} }] })}>Add row</Button>}>
          <div className={styles.editorGrid}>
            {def.rows.map((row, i) => (
              <fieldset key={i} className={styles.fieldset}>
                <legend className={styles.legend}>{`Row ${i + 1}${row.label ? `: ${row.label}` : ""}`}</legend>
                <div className={styles.editorLine}>
                  <FormField id={`fr-row-${i}-code`} label="Code">
                    {(c) => <Input {...c} className={styles.mono} value={row.code} onChange={(e) => changeRow(i, { code: e.target.value })} />}
                  </FormField>
                  <FormField id={`fr-row-${i}-kind`} label="Kind">
                    {(c) => (
                      <Select {...c} value={row.kind} onChange={(e) => changeRow(i, { kind: e.target.value as RowKind })}>
                        {ROW_KINDS.map((k) => (
                          <option key={k} value={k}>
                            {ROW_KIND_LABELS[k]}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id={`fr-row-${i}-label`} label="Label">
                    {(c) => <Input {...c} value={row.label} onChange={(e) => changeRow(i, { label: e.target.value })} />}
                  </FormField>
                  {row.kind === "account_range" ? (
                    <>
                      <FormField id={`fr-row-${i}-from`} label="Account from">
                        {(c) => <Input {...c} className={styles.mono} value={row.accountFrom ?? ""} onChange={(e) => changeRow(i, { accountFrom: e.target.value })} />}
                      </FormField>
                      <FormField id={`fr-row-${i}-to`} label="Account to">
                        {(c) => <Input {...c} className={styles.mono} value={row.accountTo ?? ""} onChange={(e) => changeRow(i, { accountTo: e.target.value })} />}
                      </FormField>
                    </>
                  ) : null}
                  {row.kind === "account_range" || row.kind === "dimension_filter" ? (
                    <FormField id={`fr-row-${i}-dims`} label="Dimension filter" optional={row.kind === "account_range"} hint="attribute=value,value; use * for any value">
                      {(c) => (
                        <Input
                          {...c}
                          key={`${i}-${formatDimensionFilter(row.dimensionFilter)}`}
                          className={styles.mono}
                          defaultValue={formatDimensionFilter(row.dimensionFilter)}
                          onBlur={(e) => changeRow(i, { dimensionFilter: parseDimensionFilter(e.target.value) })}
                        />
                      )}
                    </FormField>
                  ) : null}
                  {row.kind === "total" ? (
                    <FormField id={`fr-row-${i}-refs`} label="Rows to total" hint="Row codes separated by commas">
                      {(c) => (
                        <Input
                          {...c}
                          key={`${i}-${(row.references ?? []).join(",")}`}
                          className={styles.mono}
                          defaultValue={(row.references ?? []).join(", ")}
                          onBlur={(e) => changeRow(i, { references: parseReferences(e.target.value) })}
                        />
                      )}
                    </FormField>
                  ) : null}
                  {row.kind === "calculation" ? (
                    <FormField id={`fr-row-${i}-expr`} label="Expression" hint="Row codes with + - * / and numbers; prefix raw: for stored net">
                      {(c) => <Input {...c} className={styles.mono} value={row.expression ?? ""} onChange={(e) => changeRow(i, { expression: e.target.value })} />}
                    </FormField>
                  ) : null}
                  <FormField id={`fr-row-${i}-normal`} label="Normal balance">
                    {(c) => (
                      <Select {...c} value={row.normalBalance} onChange={(e) => changeRow(i, { normalBalance: e.target.value as "debit" | "credit" })}>
                        <option value="debit">Debit</option>
                        <option value="credit">Credit (shown with sign flipped)</option>
                      </Select>
                    )}
                  </FormField>
                  <FormField id={`fr-row-${i}-cf`} label="Cash flow tag">
                    {(c) => (
                      <Select {...c} value={row.cashFlowTag ?? ""} onChange={(e) => changeRow(i, { cashFlowTag: e.target.value })}>
                        {CASH_FLOW_TAGS.map((t) => (
                          <option key={t} value={t}>
                            {CASH_FLOW_LABELS[t]}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <FormField id={`fr-row-${i}-indent`} label="Indent">
                    {(c) => (
                      <Select {...c} value={String(row.format?.indent ?? 0)} onChange={(e) => changeRow(i, { format: { ...row.format, indent: Number(e.target.value) } })}>
                        {[0, 1, 2, 3].map((n) => (
                          <option key={n} value={n}>
                            {n}
                          </option>
                        ))}
                      </Select>
                    )}
                  </FormField>
                  <span className={styles.check}>
                    <Input id={`fr-row-${i}-bold`} type="checkbox" checked={Boolean(row.format?.bold)} onChange={(e) => changeRow(i, { format: { ...row.format, bold: e.target.checked } })} />
                    <label htmlFor={`fr-row-${i}-bold`}>Bold</label>
                  </span>
                  <span className={styles.check}>
                    <Input id={`fr-row-${i}-zero`} type="checkbox" checked={Boolean(row.format?.printZero)} onChange={(e) => changeRow(i, { format: { ...row.format, printZero: e.target.checked } })} />
                    <label htmlFor={`fr-row-${i}-zero`}>Print zero</label>
                  </span>
                  <div className={styles.lineActions}>
                    <Button size="sm" variant="ghost" aria-label={`Move row ${i + 1} up`} disabled={i === 0} onClick={() => change({ rows: move(def.rows, i, i - 1) })}>
                      Up
                    </Button>
                    <Button size="sm" variant="ghost" aria-label={`Move row ${i + 1} down`} disabled={i === def.rows.length - 1} onClick={() => change({ rows: move(def.rows, i, i + 1) })}>
                      Down
                    </Button>
                    <Button size="sm" variant="ghost" aria-label={`Remove row ${i + 1}`} disabled={def.rows.length === 1} onClick={() => change({ rows: def.rows.filter((_, j) => j !== i) })}>
                      Remove
                    </Button>
                  </div>
                </div>
              </fieldset>
            ))}
          </div>
        </Card>

        <Card
          title={`Columns (${def.columns.length})`}
          actions={<Button size="sm" onClick={() => change({ columns: [...def.columns, { code: `C${def.columns.length + 1}`, label: "", periodExpr: "P" }] })}>Add column</Button>}
        >
          <div className={styles.editorGrid}>
            {def.columns.map((col, i) => (
              <fieldset key={i} className={styles.fieldset}>
                <legend className={styles.legend}>{`Column ${i + 1}${col.label ? `: ${col.label}` : ""}`}</legend>
                <div className={styles.editorLine}>
                  <FormField id={`fr-col-${i}-code`} label="Code">
                    {(c) => <Input {...c} className={styles.mono} value={col.code} onChange={(e) => changeColumn(i, { code: e.target.value })} />}
                  </FormField>
                  <FormField id={`fr-col-${i}-label`} label="Label">
                    {(c) => <Input {...c} value={col.label} onChange={(e) => changeColumn(i, { label: e.target.value })} />}
                  </FormField>
                  <FormField id={`fr-col-${i}-period`} label="Period" hint="P, P-n, YTD, PY, PYTD, Q, FY or CALC">
                    {(c) => <Input {...c} className={styles.mono} value={col.periodExpr} onChange={(e) => changeColumn(i, { periodExpr: e.target.value.toUpperCase() })} />}
                  </FormField>
                  {col.periodExpr.trim() === "CALC" ? (
                    <FormField id={`fr-col-${i}-calc`} label="Calculation" hint="Column codes, such as C1-C2">
                      {(c) => <Input {...c} className={styles.mono} value={col.calcExpression ?? ""} onChange={(e) => changeColumn(i, { calcExpression: e.target.value })} />}
                    </FormField>
                  ) : (
                    <FormField id={`fr-col-${i}-currency`} label="Currency type">
                      {(c) => (
                        <Select {...c} value={col.currencyType ?? ""} onChange={(e) => changeColumn(i, { currencyType: (e.target.value || undefined) as CurrencyType | undefined })}>
                          <option value="">The run's currency type</option>
                          {CURRENCY_TYPES.map((t) => (
                            <option key={t} value={t}>
                              {CURRENCY_TYPE_LABELS[t]}
                            </option>
                          ))}
                        </Select>
                      )}
                    </FormField>
                  )}
                  <div className={styles.lineActions}>
                    <Button size="sm" variant="ghost" aria-label={`Remove column ${i + 1}`} disabled={def.columns.length === 1} onClick={() => change({ columns: def.columns.filter((_, j) => j !== i) })}>
                      Remove
                    </Button>
                  </div>
                </div>
              </fieldset>
            ))}
          </div>
        </Card>

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
