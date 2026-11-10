import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { comparePrices, describeProblem, listProcedures, type CompareResponse, type LineComparison, type Problem, type Procedure } from "./api/client";
import { DAY, DECIMAL, today, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

interface Draft {
  customerId: string;
  customerGroupId: string;
  currency: string;
  pricingDate: string;
  productId: string;
  unit: string;
  quantity: string;
  procedureIdA: string;
  procedureIdB: string;
}

type Errors = Record<string, string>;

/** Client checks that mirror the compare validation (FR-26.13). */
export function validateComparison(d: Draft): Errors {
  const errors: Errors = {};
  if (d.customerId.trim() === "") errors.customerId = "Enter the customer.";
  if (!/^[A-Z]{3}$/.test(d.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as USD.";
  if (!DAY.test(d.pricingDate)) errors.pricingDate = "Enter the pricing date.";
  if (d.productId.trim() === "") errors.productId = "Enter the product.";
  if (d.unit.trim() === "") errors.unit = "Enter the unit.";
  if (!DECIMAL.test(d.quantity) || Number(d.quantity) <= 0) errors.quantity = "Enter a quantity above 0.";
  if (d.procedureIdA === "") errors.procedureIdA = "Choose the baseline version.";
  if (d.procedureIdB === "") errors.procedureIdB = "Choose the version to compare.";
  else if (d.procedureIdA === d.procedureIdB) errors.procedureIdB = "Choose a different version than the baseline.";
  return errors;
}

const versionLabel = (p: Procedure) => `${p.name} v${p.version} (${p.status})`;

const signed = (delta: string) => (Number(delta) > 0 ? `+${delta}` : delta);

const columns: DataTableColumn<LineComparison>[] = [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "lineNetA", header: "Line net, baseline", mono: true, align: "end", render: (l) => (l.errorCodeA ? l.errorCodeA : l.lineNetA) },
  { key: "lineNetB", header: "Line net, compared", mono: true, align: "end", render: (l) => (l.errorCodeB ? l.errorCodeB : l.lineNetB) },
  { key: "lineNetDelta", header: "Change", mono: true, align: "end", render: (l) => signed(l.lineNetDelta) },
  { key: "appliedOnlyA", header: "Only in baseline", render: (l) => (l.appliedOnlyA.length > 0 ? l.appliedOnlyA.join(", ") : "—") },
  { key: "appliedOnlyB", header: "Only in compared", render: (l) => (l.appliedOnlyB.length > 0 ? l.appliedOnlyB.join(", ") : "—") },
];

/** Compare procedure versions (FR-26.13): price one tuple under two versions, a draft included, and read what changes. */
export function ComparePage() {
  const [legalEntityId, setLegalEntityId] = useState("");
  const [idError, setIdError] = useState("");
  const [loading, setLoading] = useState(false);
  const [versions, setVersions] = useState<{ legalEntityId: string; rows: Procedure[] } | null>(null);
  const [draft, setDraft] = useState<Draft>({ customerId: "", customerGroupId: "", currency: "USD", pricingDate: today(), productId: "", unit: "EA", quantity: "1", procedureIdA: "", procedureIdB: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<CompareResponse | null>(null);

  const set = (patch: Partial<Draft>) => setDraft((prev) => ({ ...prev, ...patch }));

  async function load(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setResult(null);
    if (!UUID.test(legalEntityId.trim())) {
      setIdError("Enter the legal entity id (a UUID).");
      return;
    }
    setIdError("");
    setLoading(true);
    try {
      const rows = await listProcedures(legalEntityId.trim());
      setVersions({ legalEntityId: legalEntityId.trim(), rows });
      set({ procedureIdA: rows[1]?.id ?? "", procedureIdB: rows[0]?.id ?? "" });
    } catch (error) {
      setVersions(null);
      setFailure(describeProblem(error));
    } finally {
      setLoading(false);
    }
  }

  async function run(event: FormEvent) {
    event.preventDefault();
    if (!versions) return;
    const found = validateComparison(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(
        await comparePrices({
          legalEntityId: versions.legalEntityId,
          customerId: draft.customerId.trim(),
          customerGroupId: draft.customerGroupId.trim() || undefined,
          currency: draft.currency,
          pricingDate: draft.pricingDate,
          lines: [{ productId: draft.productId.trim(), unit: draft.unit.trim(), quantity: draft.quantity }],
          procedureIdA: draft.procedureIdA,
          procedureIdB: draft.procedureIdB,
        }),
      );
    } catch (error) {
      const problem = describeProblem(error);
      setResult(null);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: string, name: keyof Draft, label: string, extra: { optional?: boolean; type?: string; hint?: string } = {}) => (
    <FormField id={id} label={label} optional={extra.optional} hint={extra.hint} error={errors[name]}>
      {(c) => <Input {...c} type={extra.type ?? "text"} value={draft[name]} onChange={(e) => set({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );
  const versionField = (id: string, name: "procedureIdA" | "procedureIdB", label: string, rows: Procedure[]) => (
    <FormField id={id} label={label} error={errors[name]}>
      {(c) => (
        <Select {...c} value={draft[name]} onChange={(e) => set({ [name]: e.target.value })} invalid={Boolean(errors[name])}>
          <option value="">Choose a version</option>
          {rows.map((p) => (
            <option key={p.id} value={p.id}>
              {versionLabel(p)}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Compare procedure versions" description="Price one customer, date and product under two versions of a pricing procedure, such as a draft and the active version, against the active agreements." />
      <Card>
        <form className={styles.form} onSubmit={load} noValidate>
          <FormField id="cmp-legal-entity" label="Legal entity" error={idError}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} invalid={Boolean(idError)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="secondary" loading={loading}>
              Load versions
            </Button>
          </div>
        </form>
      </Card>
      {failure ? (
        <Alert tone="danger" title="The comparison could not be completed" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {loading ? <Skeleton height="tile" /> : null}
      {!loading && !versions && !failure ? <EmptyState title="No legal entity loaded" description="Enter a legal entity id to choose the procedure versions to compare." /> : null}
      {!loading && versions && versions.rows.length < 2 ? (
        <EmptyState title="Fewer than two versions" description="A comparison needs two saved versions of a procedure. Save a draft in Pricing procedures first." />
      ) : null}
      {!loading && versions && versions.rows.length >= 2 ? (
        <Card title="Comparison">
          <form className={styles.form} onSubmit={run} noValidate>
            {versionField("cmp-a", "procedureIdA", "Baseline version", versions.rows)}
            {versionField("cmp-b", "procedureIdB", "Version to compare", versions.rows)}
            {field("cmp-customer", "customerId", "Customer")}
            {field("cmp-group", "customerGroupId", "Customer group", { optional: true })}
            {field("cmp-currency", "currency", "Currency")}
            {field("cmp-date", "pricingDate", "Pricing date", { type: "date" })}
            {field("cmp-product", "productId", "Product")}
            {field("cmp-unit", "unit", "Unit")}
            {field("cmp-qty", "quantity", "Quantity")}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Compare versions
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {result ? (
        <>
          <StatGroup label="Comparison totals">
            <StatTile label="Baseline order net" value={`${result.a.orderNet} ${result.a.currency}`} />
            <StatTile label="Compared order net" value={`${result.b.orderNet} ${result.b.currency}`} />
            <StatTile label="Change" value={`${signed(result.comparison.orderNetDelta)} ${result.a.currency}`} />
            <StatTile label="Lines that differ" value={String(result.comparison.changedLines)} />
          </StatGroup>
          {result.comparison.identical ? (
            <Alert tone="success" title="No difference" live="status">
              Both versions price this request identically.
            </Alert>
          ) : null}
          <Card title="Line by line">
            <DataTable label="Compared lines" columns={columns} rows={result.comparison.lines} getRowKey={(l) => String(l.lineNo)} />
          </Card>
        </>
      ) : null}
    </div>
  );
}
