import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  AUDIT_RULE_TYPES,
  describeProblem,
  evaluateAuditPolicy,
  type AuditDocument,
  type AuditEvaluateInput,
  type AuditEvaluateResult,
  type AuditFinding,
  type AuditRuleType,
  type AuditSpec,
  type Problem,
} from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";

export const ruleLabel: Record<AuditRuleType, string> = {
  DUPLICATE: "Duplicate vendor invoice",
  KEYWORD: "Keyword",
  LIST_SEARCH: "Watch list",
  SAMPLING: "Sampling",
  THRESHOLD: "Amount above limit",
  WEEKEND_HOLIDAY: "Weekend or holiday",
  AGGREGATE: "Monthly total above limit",
};

const ruleHelp: Record<AuditRuleType, string> = {
  DUPLICATE: "Same vendor and currency, amount within 0.5% (at least 0.01), dated within 30 days, invoice numbers equal or one character apart. The later invoice is flagged.",
  KEYWORD: "Flags documents whose text contains any of the words, ignoring case and punctuation.",
  LIST_SEARCH: "Flags documents whose party is on the watch list, for example employees set up as vendors.",
  SAMPLING: "Picks documents for review by a seeded draw; the same seed always picks the same documents.",
  THRESHOLD: "Flags documents above the limit for their category, in the limit currency.",
  WEEKEND_HOLIDAY: "Flags documents dated on a Saturday, Sunday or a listed holiday (UTC day).",
  AGGREGATE: "Flags every document of a party whose monthly total is above the limit.",
};

const DECIMAL = /^\d+(\.\d+)?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;

/**
 * Parses one document per line, fields separated by "|":
 * id | party | category | amount | currency | YYYY-MM-DD | invoice number | text.
 * Trailing fields may be left out; blank lines are skipped.
 */
export function parseDocuments(text: string): { documents: AuditDocument[]; error?: string } {
  const documents: AuditDocument[] = [];
  const seen = new Set<string>();
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].trim()) continue;
    const [id = "", party = "", category = "", amount = "", currency = "", date = "", number = "", ...rest] = lines[i].split("|").map((f) => f.trim());
    const at = `Line ${i + 1}`;
    if (!id) return { documents: [], error: `${at}: start with the document id.` };
    if (seen.has(id)) return { documents: [], error: `${at}: document ${id} is listed twice.` };
    if (amount && !DECIMAL.test(amount)) return { documents: [], error: `${at}: the amount must be a number such as 1250.50.` };
    if (currency && !CURRENCY.test(currency)) return { documents: [], error: `${at}: the currency must be a three-letter code such as USD.` };
    if (date && (!DATE.test(date) || Number.isNaN(new Date(`${date}T00:00:00Z`).getTime()))) return { documents: [], error: `${at}: the date must be YYYY-MM-DD.` };
    seen.add(id);
    const doc: AuditDocument = { id };
    if (party) doc.partyId = party;
    if (category) doc.category = category;
    if (amount) doc.amount = amount;
    if (currency) doc.currency = currency;
    if (date) doc.date = `${date}T00:00:00Z`;
    if (number) doc.number = number;
    const body = rest.join("|").trim();
    if (body) doc.texts = [body];
    documents.push(doc);
  }
  return { documents };
}

function list(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export interface AuditValues {
  policyId: string;
  version: string;
  type: AuditRuleType;
  words: string;
  currency: string;
  limit: string;
  limits: string;
  holidays: string;
  seed: string;
  sampleBy: "percent" | "everyK";
  sampleSize: string;
  documents: string;
}

type Field = Exclude<keyof AuditValues, "type" | "sampleBy">;
type Errors = Partial<Record<Field, string>>;

/** Checks the form and builds the request; amounts stay decimal strings. */
export function toAuditInput(v: AuditValues): { input?: AuditEvaluateInput; errors: Errors } {
  const errors: Errors = {};
  if (!UUID.test(v.policyId.trim())) errors.policyId = "Enter the policy's UUID.";
  const version = Number(v.version.trim());
  if (!Number.isInteger(version) || version < 1) errors.version = "Enter a whole version number from 1.";
  const spec: AuditSpec = { type: v.type };
  const currency = v.currency.trim().toUpperCase();
  switch (v.type) {
    case "KEYWORD":
    case "LIST_SEARCH": {
      const words = list(v.words);
      if (words.length === 0) errors.words = v.type === "KEYWORD" ? "Enter at least one keyword." : "Enter at least one watch-list entry.";
      if (v.type === "KEYWORD") spec.keywords = words;
      else spec.watchList = words;
      break;
    }
    case "THRESHOLD": {
      if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code.";
      const limits: Record<string, string> = {};
      for (const [i, line] of v.limits.split("\n").entries()) {
        if (!line.trim()) continue;
        const [category = "", amount = ""] = line.split("=").map((s) => s.trim());
        if (!category || !DECIMAL.test(amount) || Number(amount) <= 0) {
          errors.limits = `Line ${i + 1}: write "CATEGORY = positive amount".`;
          break;
        }
        limits[category] = amount;
      }
      if (!errors.limits && Object.keys(limits).length === 0) errors.limits = "Enter a limit for at least one category.";
      spec.currency = currency;
      spec.limits = limits;
      break;
    }
    case "AGGREGATE":
      if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code.";
      if (!DECIMAL.test(v.limit.trim()) || Number(v.limit) <= 0) errors.limit = "Enter a positive monthly limit.";
      spec.currency = currency;
      spec.limit = v.limit.trim();
      break;
    case "WEEKEND_HOLIDAY": {
      const holidays = list(v.holidays);
      const bad = holidays.find((h) => !DATE.test(h));
      if (bad) errors.holidays = `${bad} is not a YYYY-MM-DD date.`;
      if (holidays.length > 0) spec.holidays = holidays;
      break;
    }
    case "SAMPLING": {
      if (!v.seed.trim()) errors.seed = "Enter the seed stored with the run.";
      const n = Number(v.sampleSize.trim());
      const ok = Number.isInteger(n) && n >= 1 && (v.sampleBy === "everyK" || n <= 100);
      if (!ok) errors.sampleSize = v.sampleBy === "percent" ? "Enter a whole percentage from 1 to 100." : "Enter a whole number from 1.";
      spec.sample = v.sampleBy === "percent" ? { seed: v.seed.trim(), percent: n } : { seed: v.seed.trim(), everyK: n };
      break;
    }
    case "DUPLICATE":
      break;
  }
  const parsed = parseDocuments(v.documents);
  if (parsed.error) errors.documents = parsed.error;
  else if (v.type === "DUPLICATE" || v.type === "WEEKEND_HOLIDAY" || v.type === "AGGREGATE") {
    const undated = parsed.documents.find((d) => !d.date);
    if (undated) errors.documents = `Document ${undated.id} needs a date for this rule.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { input: { policyId: v.policyId.trim().toLowerCase(), version, spec, documents: parsed.documents }, errors };
}

function evidenceText(f: AuditFinding): string {
  const e = f.evidence ?? {};
  switch (f.ruleType) {
    case "DUPLICATE":
      return `Repeats ${e.duplicateOf ?? "an earlier invoice"}`;
    case "KEYWORD":
      return `Mentions ${e.keywords ?? ""}`;
    case "LIST_SEARCH":
      return `Party ${e.partyId ?? ""} is on the watch list`;
    case "THRESHOLD":
      return `${e.amount} against a ${e.category} limit of ${e.limit} (${e.pctOfLimit}%)`;
    case "WEEKEND_HOLIDAY":
      return `Dated ${e.date}`;
    case "AGGREGATE":
      return `${e.partyId} totals ${e.total} in ${e.period}`;
    case "SAMPLING":
      return "Selected for review";
  }
  return Object.entries(e)
    .map(([k, val]) => `${k}: ${val}`)
    .join(", ");
}

const columns: DataTableColumn<AuditFinding>[] = [
  { key: "documentId", header: "Document", mono: true },
  { key: "evidence", header: "Why", render: (f) => evidenceText(f) },
  { key: "score", header: "Score", align: "end" },
  { key: "key", header: "Violation key", render: (f) => <span className={`${styles.mono} ${styles.muted}`}>{f.key}</span> },
];

const initial: AuditValues = {
  policyId: "",
  version: "1",
  type: "DUPLICATE",
  words: "",
  currency: "",
  limit: "",
  limits: "",
  holidays: "",
  seed: "",
  sampleBy: "percent",
  sampleSize: "",
  documents: "",
};

/**
 * Audit rule run (PRD §9.48.6 Audit policies, duty compliance.audit-policy.run): runs one
 * audit rule over a document window and lists the violations it would create
 * (FR-48.14). Each violation key is fixed by policy, version and document, so
 * a rerun creates no new violations. Nothing is stored yet.
 */
export function AuditRunPage() {
  const [values, setValues] = useState<AuditValues>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<AuditEvaluateResult | null>(null);

  function set<K extends keyof AuditValues>(name: K, value: AuditValues[K]) {
    setValues((v) => ({ ...v, [name]: value }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toAuditInput(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      setResult(await evaluateAuditPolicy(input));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const t = values.type;
  const summary = result
    ? result.count === 0
      ? "No document violates this rule."
      : `${result.count} ${result.count === 1 ? "document violates" : "documents violate"} the policy (${result.code ?? "AUD-0701"}).`
    : null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Run an audit rule"
        description="Check a window of expenses, vendor invoices, purchase orders or journals against one audit rule and see the violations it would raise. Nothing is saved yet."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Audit rule">
          <FormField id="audit-policy" label="Policy id" error={errors.policyId}>
            {(control) => <Input {...control} value={values.policyId} onChange={(e) => set("policyId", e.target.value)} invalid={Boolean(errors.policyId)} />}
          </FormField>
          <FormField id="audit-version" label="Policy version" error={errors.version}>
            {(control) => <Input {...control} inputMode="numeric" value={values.version} onChange={(e) => set("version", e.target.value)} invalid={Boolean(errors.version)} />}
          </FormField>
          <FormField id="audit-type" label="Rule" hint={ruleHelp[t]}>
            {(control) => (
              <Select {...control} value={t} onChange={(e) => set("type", e.target.value as AuditRuleType)}>
                {AUDIT_RULE_TYPES.map((r) => (
                  <option key={r} value={r}>
                    {ruleLabel[r]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {t === "KEYWORD" || t === "LIST_SEARCH" ? (
            <FormField id="audit-words" label={t === "KEYWORD" ? "Keywords" : "Watch list"} hint="Separate entries with commas or new lines." error={errors.words}>
              {(control) => <Input {...control} value={values.words} onChange={(e) => set("words", e.target.value)} invalid={Boolean(errors.words)} />}
            </FormField>
          ) : null}
          {t === "THRESHOLD" || t === "AGGREGATE" ? (
            <FormField id="audit-currency" label="Limit currency" error={errors.currency}>
              {(control) => <Input {...control} maxLength={3} value={values.currency} onChange={(e) => set("currency", e.target.value.toUpperCase())} invalid={Boolean(errors.currency)} />}
            </FormField>
          ) : null}
          {t === "AGGREGATE" ? (
            <FormField id="audit-limit" label="Monthly limit per party" error={errors.limit}>
              {(control) => <Input {...control} inputMode="decimal" value={values.limit} onChange={(e) => set("limit", e.target.value)} invalid={Boolean(errors.limit)} />}
            </FormField>
          ) : null}
          {t === "THRESHOLD" ? (
            <div className={styles.wide}>
              <FormField id="audit-limits" label="Limits by category" hint='One per line, for example "MEALS = 200".' error={errors.limits}>
                {(control) => <Textarea {...control} rows={3} mono value={values.limits} onChange={(e) => set("limits", e.target.value)} invalid={Boolean(errors.limits)} />}
              </FormField>
            </div>
          ) : null}
          {t === "WEEKEND_HOLIDAY" ? (
            <FormField id="audit-holidays" label="Holidays" hint="YYYY-MM-DD, separated by commas." error={errors.holidays} optional>
              {(control) => <Input {...control} value={values.holidays} onChange={(e) => set("holidays", e.target.value)} invalid={Boolean(errors.holidays)} />}
            </FormField>
          ) : null}
          {t === "SAMPLING" ? (
            <>
              <FormField id="audit-seed" label="Seed" hint="Stored with the run so a rerun selects the same documents." error={errors.seed}>
                {(control) => <Input {...control} value={values.seed} onChange={(e) => set("seed", e.target.value)} invalid={Boolean(errors.seed)} />}
              </FormField>
              <FormField id="audit-sample-by" label="Sample by">
                {(control) => (
                  <Select {...control} value={values.sampleBy} onChange={(e) => set("sampleBy", e.target.value as AuditValues["sampleBy"])}>
                    <option value="percent">Percentage of documents</option>
                    <option value="everyK">Every k-th document</option>
                  </Select>
                )}
              </FormField>
              <FormField id="audit-sample-size" label={values.sampleBy === "percent" ? "Percentage" : "k"} error={errors.sampleSize}>
                {(control) => <Input {...control} inputMode="numeric" value={values.sampleSize} onChange={(e) => set("sampleSize", e.target.value)} invalid={Boolean(errors.sampleSize)} />}
              </FormField>
            </>
          ) : null}
          <div className={styles.wide}>
            <FormField
              id="audit-documents"
              label="Documents"
              hint="One per line: id | party | category | amount | currency | YYYY-MM-DD | invoice number | text. Trailing fields may be left out."
              error={errors.documents}
              optional
            >
              {(control) => <Textarea {...control} rows={6} mono value={values.documents} onChange={(e) => set("documents", e.target.value)} invalid={Boolean(errors.documents)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run rule
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The rule could not be run" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone={result && result.count > 0 ? "warning" : "success"} live="status">
        {summary}
      </Alert>
      {result ? (
        <DataTable
          label="Audit violations"
          columns={columns}
          rows={result.findings}
          getRowKey={(f) => f.key}
          empty={{ title: "No violations", description: "Every document passes this rule." }}
        />
      ) : (
        <Card>
          <EmptyState icon="info" title="No run yet" description="Choose a rule, paste the documents to check and run it." compact />
        </Card>
      )}
    </div>
  );
}
