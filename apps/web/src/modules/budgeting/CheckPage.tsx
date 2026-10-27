import { useRef, useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { checkBudget, describeError, getCheck, listRules, type CheckLineOutcome, type CheckOutcome } from "./api/client";
import { blockedLineIds, DOCUMENT_TYPES, formatAmount, overPercentLabel, UUID, validateCheckRows, type CheckRow, type DocumentType } from "./logic";
import styles from "./pages.module.css";
import { dash, Detail, formatDateTime, LoadError, ResultBadge } from "./parts";
import { useLoad } from "./useLoad";

const emptyRow = (): CheckRow => ({ lineId: crypto.randomUUID(), dimensionKey: "", spanKey: "", amount: "" });

const lineColumns: DataTableColumn<CheckLineOutcome>[] = [
  { key: "dimensionKey", header: "Dimension", mono: true },
  { key: "spanKey", header: "Time span", mono: true },
  { key: "amount", header: "Amount", align: "end", mono: true, render: (l) => formatAmount(l.amount) },
  { key: "prior", header: "Prior reservation", align: "end", mono: true, render: (l) => formatAmount(l.prior) },
  { key: "net", header: "Net change", align: "end", mono: true, render: (l) => formatAmount(l.net) },
  { key: "available", header: "Available", align: "end", mono: true, render: (l) => formatAmount(l.available) },
  { key: "overPct", header: "Over", align: "end", mono: true, render: (l) => overPercentLabel(l.overPct) },
  { key: "result", header: "Result", render: (l) => <ResultBadge result={l.result} /> },
  {
    key: "encumbranceId",
    header: "Encumbrance",
    render: (l) => (l.encumbranceId ? <Link to={`/budgeting/encumbrances/${l.encumbranceId}`}>Open</Link> : dash),
  },
];

/** Per-line outcome of a check with its FR-38.4 step trace. */
function CheckResult({ outcome }: { outcome: CheckOutcome }) {
  const blocked = blockedLineIds(outcome).length;
  return (
    <div className={styles.stack}>
      <Card>
        <dl className={styles.details}>
          <Detail term="Result">
            <ResultBadge result={outcome.result} />
          </Detail>
          <Detail term="Document" mono>
            {outcome.documentId}
          </Detail>
          <Detail term="Document type">{outcome.documentType}</Detail>
          <Detail term="Blocked lines">{blocked}</Detail>
          <Detail term="Override applied">{outcome.overridden ? "Yes" : "No"}</Detail>
          <Detail term="Checked">{formatDateTime(outcome.checkedAt)}</Detail>
        </dl>
      </Card>
      <DataTable label="Check lines" columns={lineColumns} rows={outcome.lines} getRowKey={(l) => l.lineId} />
      <p className={styles.note}>
        Steps per line: {outcome.lines.map((l, i) => `line ${i + 1}: ${l.trace.join(" → ")}`).join("; ")}
      </p>
    </div>
  );
}

/** Budget check form (FR-38.4). A line already reserved is checked for its net change only. */
export function BudgetCheckPage() {
  const [params] = useSearchParams();
  const rules = useLoad((signal) => listRules(undefined, signal), "rules");
  const [ruleVersionId, setRuleVersionId] = useState(params.get("rule") ?? "");
  const [documentId, setDocumentId] = useState<string>(() => crypto.randomUUID());
  const [documentType, setDocumentType] = useState<DocumentType>("PurchaseOrder");
  const [rows, setRows] = useState<CheckRow[]>([emptyRow()]);
  const [errors, setErrors] = useState<string[]>([]);
  const [outcome, setOutcome] = useState<CheckOutcome | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  function updateRow(index: number, patch: Partial<CheckRow>) {
    setRows((current) => current.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const clean = rows.map((r) => ({ ...r, dimensionKey: r.dimensionKey.trim(), spanKey: r.spanKey.trim(), amount: r.amount.trim() }));
    const valid = validateCheckRows(clean);
    const found = valid.ok ? [] : [...valid.errors];
    if (!UUID.test(ruleVersionId)) found.unshift("Choose a control rule version.");
    if (!UUID.test(documentId.trim())) found.unshift("The document id must be a UUID.");
    setErrors(found);
    if (found.length > 0) return;
    const input = { ruleVersionId, documentId: documentId.trim(), documentType, lines: clean };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      setOutcome(await checkBudget(input, key.current.value));
    } catch (err) {
      setOutcome(null);
      setFailure(describeError(err, "The check could not be run."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Budget check"
        description="Check a requisition, purchase order, invoice or journal against available funds. Pass and warn reserve the amount; fail reserves nothing. Re-checking the same lines checks only the change."
      />
      {rules.error ? <LoadError title="Could not load control rules" error={rules.error} onRetry={rules.reload} /> : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="check-rule" label="Control rule version">
            {(c) => (
              <Select {...c} value={ruleVersionId} onChange={(e) => setRuleVersionId(e.target.value)}>
                <option value="">{rules.loading && !rules.data ? "Loading rules…" : "Choose a rule"}</option>
                {(rules.data?.items ?? []).map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.code} v{r.version} ({r.currency}, warn {r.warnPct}% / block {r.blockPct}%)
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="check-document" label="Document id" hint="Keep the same id and line ids to re-check a revised document.">
            {(c) => <Input {...c} className={styles.mono} value={documentId} onChange={(e) => setDocumentId(e.target.value)} />}
          </FormField>
          <FormField id="check-type" label="Document type">
            {(c) => (
              <Select {...c} value={documentType} onChange={(e) => setDocumentType(e.target.value as DocumentType)}>
                {DOCUMENT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {rows.map((row, i) => (
            <fieldset key={row.lineId} className={styles.lineSet}>
              <legend className={styles.legend}>
                Line {i + 1} <span className={styles.mono}>{row.lineId}</span>
              </legend>
              <FormField id={`check-dim-${i}`} label="Dimension">
                {(c) => <Input {...c} className={styles.mono} value={row.dimensionKey} onChange={(e) => updateRow(i, { dimensionKey: e.target.value })} />}
              </FormField>
              <FormField id={`check-span-${i}`} label="Time span" hint="For example FY2026.">
                {(c) => <Input {...c} className={styles.mono} value={row.spanKey} onChange={(e) => updateRow(i, { spanKey: e.target.value })} />}
              </FormField>
              <FormField id={`check-amount-${i}`} label="Amount">
                {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={row.amount} onChange={(e) => updateRow(i, { amount: e.target.value })} />}
              </FormField>
              {rows.length > 1 ? (
                <Button variant="ghost" onClick={() => setRows((current) => current.filter((_, k) => k !== i))}>
                  Remove line {i + 1}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button onClick={() => setRows((current) => [...current, emptyRow()])}>Add line</Button>
            <Button type="submit" variant="primary" loading={busy}>
              Run check
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                setDocumentId(crypto.randomUUID());
                setRows([emptyRow()]);
                setOutcome(null);
              }}
            >
              New document
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {errors.length > 0 ? (
          <ul className={styles.errorList}>
            {errors.map((msg) => (
              <li key={msg}>{msg}</li>
            ))}
          </ul>
        ) : (
          failure
        )}
      </Alert>
      {outcome ? <CheckResult outcome={outcome} /> : null}
    </div>
  );
}

/** One recorded check (FR-38.4 step 8), including failed ones. */
export function CheckDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCheck(id, signal), id);
  return (
    <div className={styles.stack}>
      <PageHeader title="Budget check result" description="The recorded outcome of one check, with each line's available funds and step trace." />
      {state.error ? (
        <LoadError title="Could not load this check" error={state.error} onRetry={state.reload} />
      ) : state.data ? (
        <CheckResult outcome={state.data} />
      ) : (
        <div aria-busy="true">
          <Skeleton height="tile" />
          <span className="visually-hidden">Loading…</span>
        </div>
      )}
    </div>
  );
}
