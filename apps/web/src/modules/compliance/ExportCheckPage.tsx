import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { checkExport, describeProblem, EXPORT_REGIMES, type ExportCheckInput, type ExportItem, type ExportLicense, type ExportLineDecision, type ExportRegime, type ExportResult, type Problem } from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";

const DECIMAL = /^\d+(\.\d+)?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const COUNTRY = /^[A-Za-z]{2}$/;
const REGION = /^[A-Za-z]{2}-[A-Za-z0-9]{1,3}$/;

function regimeOf(value: string): ExportRegime | undefined {
  const r = value.trim().toUpperCase();
  return (EXPORT_REGIMES as readonly string[]).includes(r) ? (r as ExportRegime) : undefined;
}

function list(text: string): string[] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

/**
 * Parses one delivery line per line, fields separated by "|":
 * item id | regime (US_EAR, EU_DUALUSE or HS) | classification code | quantity | value.
 */
export function parseItems(text: string): { items: ExportItem[]; error?: string } {
  const items: ExportItem[] = [];
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].trim()) continue;
    const [itemId = "", regime = "", code = "", qty = "", value = ""] = lines[i].split("|").map((f) => f.trim());
    const at = `Line ${i + 1}`;
    if (!itemId) return { items: [], error: `${at}: start with the item id.` };
    const r = regimeOf(regime);
    if (!r) return { items: [], error: `${at}: the regime must be US_EAR, EU_DUALUSE or HS.` };
    if (!code) return { items: [], error: `${at}: give the classification code, for example 5A002 or EAR99.` };
    if (!DECIMAL.test(qty) || !DECIMAL.test(value)) return { items: [], error: `${at}: quantity and value must be numbers such as 2 and 1800.00.` };
    items.push({ itemId, classification: { regime: r, code: code.toUpperCase() }, qty, value });
  }
  return { items };
}

/**
 * Parses one license per line, fields separated by "|":
 * license id | number | regime | classifications | destinations | quantity limit | used quantity |
 * value limit | used value | expires YYYY-MM-DD. Classifications ("US_EAR:5A002") and destinations
 * ("CN, IN") are comma-separated; an empty limit means none; used amounts default to 0.
 */
export function parseLicenses(text: string): { licenses: ExportLicense[]; error?: string } {
  const licenses: ExportLicense[] = [];
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].trim()) continue;
    const [licenseId = "", number = "", regime = "", classes = "", dests = "", qtyLimit = "", usedQty = "", valueLimit = "", usedValue = "", expires = ""] = lines[i]
      .split("|")
      .map((f) => f.trim());
    const at = `Line ${i + 1}`;
    if (!licenseId) return { licenses: [], error: `${at}: start with the license id.` };
    const r = regimeOf(regime);
    if (!r) return { licenses: [], error: `${at}: the regime must be US_EAR, EU_DUALUSE or HS.` };
    const classifications = list(classes).map((c) => c.toUpperCase());
    if (classifications.length === 0 || classifications.some((c) => !c.includes(":"))) return { licenses: [], error: `${at}: list classifications as REGIME:CODE, for example US_EAR:5A002.` };
    const destinations = list(dests).map((d) => d.toUpperCase());
    if (destinations.length === 0 || destinations.some((d) => !COUNTRY.test(d))) return { licenses: [], error: `${at}: list destinations as two-letter codes such as CN, IN.` };
    for (const amount of [qtyLimit, usedQty, valueLimit, usedValue]) {
      if (amount && !DECIMAL.test(amount)) return { licenses: [], error: `${at}: limits and used amounts must be numbers.` };
    }
    if (!DATE.test(expires) || Number.isNaN(new Date(`${expires}T00:00:00Z`).getTime())) return { licenses: [], error: `${at}: the expiry must be YYYY-MM-DD.` };
    const license: ExportLicense = {
      licenseId,
      regime: r,
      classifications,
      destinations,
      usedQty: usedQty || "0",
      usedValue: usedValue || "0",
      // The license is valid through the end of its expiry day, UTC.
      expiresAt: `${expires}T23:59:59Z`,
    };
    if (number) license.number = number;
    if (qtyLimit) license.qtyLimit = qtyLimit;
    if (valueLimit) license.valueLimit = valueLimit;
    licenses.push(license);
  }
  return { licenses };
}

export interface ExportValues {
  destination: string;
  region: string;
  items: string;
  licenses: string;
  releaseRef: string;
}

type Errors = Partial<Record<keyof ExportValues, string>>;

export function toExportInput(v: ExportValues): { input?: ExportCheckInput; errors: Errors } {
  const errors: Errors = {};
  const destination = v.destination.trim().toUpperCase();
  const region = v.region.trim().toUpperCase();
  if (!COUNTRY.test(destination)) errors.destination = "Use a two-letter country code such as CN.";
  if (region && (!REGION.test(region) || !region.startsWith(`${destination}-`))) errors.region = "Use a region code of the destination, for example UA-43.";
  const items = parseItems(v.items);
  if (items.error) errors.items = items.error;
  else if (items.items.length === 0) errors.items = "Enter at least one delivery line.";
  const licenses = parseLicenses(v.licenses);
  if (licenses.error) errors.licenses = licenses.error;
  if (Object.keys(errors).length > 0) return { errors };
  const input: ExportCheckInput = { destination, items: items.items, licenses: licenses.licenses };
  if (region) input.region = region;
  if (v.releaseRef.trim()) input.officerReleaseLicenseRef = v.releaseRef.trim();
  return { input, errors };
}

function outcome(d: ExportLineDecision) {
  if (d.blocked) return <Badge tone="danger">Blocked</Badge>;
  if (d.warning) return <Badge tone="warning">Warning</Badge>;
  return <Badge tone="success">May ship</Badge>;
}

const columns: DataTableColumn<ExportLineDecision>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "licenseRequired", header: "License needed", render: (d) => (d.licenseRequired ? "Yes" : "No") },
  { key: "licenseId", header: "License", mono: true, render: (d) => d.licenseId ?? dash },
  { key: "outcome", header: "Outcome", render: outcome },
  { key: "detail", header: "Why", render: (d) => (d.code ? `${d.detail ?? ""} (${d.code})` : dash) },
];

const initial: ExportValues = { destination: "", region: "", items: "", licenses: "", releaseRef: "" };

/**
 * Export compliance check (PRD §9.48.6, FR-48.20, duty TradeComplianceView):
 * the license determination of a delivery. An embargoed destination blocks
 * every line (TRD-1002); a line that needs a license blocks without a valid
 * one with quantity and value left (TRD-1003). Nothing is stored yet.
 */
export function ExportCheckPage() {
  const [values, setValues] = useState<ExportValues>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ExportResult | null>(null);

  function set<K extends keyof ExportValues>(name: K, value: ExportValues[K]) {
    setValues((v) => ({ ...v, [name]: value }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toExportInput(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      setResult(await checkExport(input));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const blockedLines = result?.items.filter((d) => d.blocked).length ?? 0;
  const summary = result
    ? result.embargo && result.blocked
      ? `The destination is under embargo; the delivery is blocked (TRD-1002).`
      : result.blocked
        ? `${blockedLines} of ${result.items.length} ${result.items.length === 1 ? "line is" : "lines are"} blocked.`
        : result.items.some((d) => d.warning)
          ? "The delivery may ship, with license warnings."
          : "The delivery may ship."
    : null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Export license check"
        description="See whether a delivery needs an export license for each line, and whether a license on file covers it, before the delivery is confirmed."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Export license check">
          <FormField id="export-destination" label="Destination country" error={errors.destination}>
            {(control) => <Input {...control} maxLength={2} value={values.destination} onChange={(e) => set("destination", e.target.value.toUpperCase())} invalid={Boolean(errors.destination)} />}
          </FormField>
          <FormField id="export-region" label="Destination region" hint="ISO 3166-2, for example UA-43 for Crimea." optional error={errors.region}>
            {(control) => <Input {...control} value={values.region} onChange={(e) => set("region", e.target.value.toUpperCase())} invalid={Boolean(errors.region)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="export-items" label="Delivery lines" hint="One per line: item id | US_EAR, EU_DUALUSE or HS | code | quantity | value." error={errors.items}>
              {(control) => <Textarea {...control} rows={4} mono value={values.items} onChange={(e) => set("items", e.target.value)} invalid={Boolean(errors.items)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="export-licenses"
              label="Licenses on file"
              hint="One per line: id | number | regime | REGIME:CODE, … | destinations, … | quantity limit | used quantity | value limit | used value | expires YYYY-MM-DD. Leave a limit empty for none."
              optional
              error={errors.licenses}
            >
              {(control) => <Textarea {...control} rows={3} mono value={values.licenses} onChange={(e) => set("licenses", e.target.value)} invalid={Boolean(errors.licenses)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="export-release"
              label="Embargo release license reference"
              hint="Only a trade compliance officer can release a delivery to an embargoed destination, and only with a license reference."
              optional
            >
              {(control) => <Input {...control} className={styles.mono} value={values.releaseRef} onChange={(e) => set("releaseRef", e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check delivery
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The export check failed" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone={result?.blocked ? "danger" : result?.items.some((d) => d.warning) ? "warning" : "success"} live="status">
        {summary}
      </Alert>
      {result ? (
        <DataTable label="License determination" columns={columns} rows={result.items} getRowKey={(d) => String(result.items.indexOf(d))} empty={{ title: "No lines", description: "The delivery had no lines." }} />
      ) : (
        <Card>
          <EmptyState icon="info" title="No check yet" description="Enter the destination and the delivery lines, then check." compact />
        </Card>
      )}
    </div>
  );
}
