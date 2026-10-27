import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  previewSubcontract as defaultPreview,
  SUBCONTRACT_MODES,
  type PoLine,
  type Problem,
  type SubcontractMode,
  type SubcontractRequest,
} from "./api/client";
import { positiveQuantity, QUANTITY } from "./format";
import styles from "./pages.module.css";

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;

const MODE_LABELS: Record<SubcontractMode, string> = {
  "po-per-kanban": "One PO line per kanban job",
  "periodic-po": "One PO line per vendor, service and price for the period",
};

interface AgreementRow {
  vendorId: string;
  itemId: string;
  unitPrice: string;
  currency: string;
  validFrom: string;
  validTo: string;
}

interface JobRow {
  jobId: string;
  vendorId: string;
  itemId: string;
  qty: string;
  completedOn: string;
}

function useRows<T>(initial: T[], blank: () => T) {
  const [rows, setRows] = useState<T[]>(initial);
  return {
    rows,
    update: (i: number, patch: Partial<T>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r))),
    add: () => setRows((rs) => [...rs, blank()]),
    remove: (i: number) => setRows((rs) => rs.filter((_, j) => j !== i)),
  };
}

const lineColumns: DataTableColumn<PoLine>[] = [
  { key: "vendorId", header: "Vendor", mono: true },
  { key: "itemId", header: "Service item", mono: true },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "unitPrice", header: "Unit price", align: "end" },
  { key: "amount", header: "Amount", align: "end", render: (l) => `${l.amount} ${l.currency}` },
  { key: "priceDate", header: "Priced on" },
  { key: "jobIds", header: "Kanban jobs", render: (l) => l.jobIds.join(", ") },
];

/**
 * Lean subcontracting (FR-24.20, BR-24.16): the purchase order lines that
 * completed service jobs on vendor work cells produce, each priced by the
 * purchase agreement valid on its completion date. A preview; the PO is
 * created and received in purchasing in a later slice.
 */
export function SubcontractPage({ preview = defaultPreview }: { preview?: typeof defaultPreview }) {
  // AC-24.17: a job of 20 on vendor V at the agreement price.
  const [mode, setMode] = useState<SubcontractMode>("po-per-kanban");
  const agreements = useRows<AgreementRow>(
    [{ vendorId: "V-PLATING", itemId: "SVC-PLATING", unitPrice: "4.50", currency: "USD", validFrom: "2027-01-01", validTo: "" }],
    () => ({ vendorId: "", itemId: "", unitPrice: "", currency: "USD", validFrom: "", validTo: "" }),
  );
  const jobs = useRows<JobRow>(
    [{ jobId: "KJ-0001", vendorId: "V-PLATING", itemId: "SVC-PLATING", qty: "20", completedOn: "2027-03-10" }],
    () => ({ jobId: "", vendorId: "", itemId: "", qty: "", completedOn: "" }),
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [lines, setLines] = useState<PoLine[] | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    agreements.rows.forEach((a, i) => {
      if (!a.vendorId.trim()) found[`a-${i}-vendor`] = "Enter the vendor.";
      if (!a.itemId.trim()) found[`a-${i}-item`] = "Enter the service item.";
      if (!QUANTITY.test(a.unitPrice.trim())) found[`a-${i}-price`] = "Enter a price with at most 6 decimals.";
      if (!CURRENCY.test(a.currency.trim().toUpperCase())) found[`a-${i}-currency`] = "Enter a currency code such as USD.";
      if (!DATE.test(a.validFrom)) found[`a-${i}-from`] = "Enter the first valid day.";
      if (a.validTo !== "" && a.validTo < a.validFrom) found[`a-${i}-to`] = "The last valid day is before the first.";
    });
    jobs.rows.forEach((j, i) => {
      if (!j.jobId.trim()) found[`j-${i}-id`] = "Enter the kanban job.";
      if (!j.vendorId.trim()) found[`j-${i}-vendor`] = "Enter the vendor.";
      if (!j.itemId.trim()) found[`j-${i}-item`] = "Enter the service item.";
      if (!positiveQuantity(j.qty.trim())) found[`j-${i}-qty`] = "Enter the completed quantity.";
      if (!DATE.test(j.completedOn)) found[`j-${i}-done`] = "Enter the completion day.";
    });
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const req: SubcontractRequest = {
      mode,
      agreements: agreements.rows.map((a) => ({
        vendorId: a.vendorId.trim(),
        itemId: a.itemId.trim(),
        unitPrice: a.unitPrice.trim(),
        currency: a.currency.trim().toUpperCase(),
        validFrom: a.validFrom,
        validTo: a.validTo || null,
      })),
      jobs: jobs.rows.map((j) => ({ jobId: j.jobId.trim(), vendorId: j.vendorId.trim(), itemId: j.itemId.trim(), qty: j.qty.trim(), completedOn: j.completedOn })),
    };
    setBusy(true);
    try {
      setLines((await preview(req)).lines);
    } catch (error) {
      setLines(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  function field(id: string, label: string, value: string, onChange: (v: string) => void, opts: { type?: string; decimal?: boolean } = {}) {
    return (
      <FormField id={id} label={label} error={errors[id]}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            invalid={Boolean(errors[id])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Lean subcontracting"
        description="Purchase order lines for service activities on vendor work cells: each completed kanban job is priced by the purchase agreement valid on the day it completed."
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Purchase order mode">
          <div className={styles.form}>
            <FormField id="sub-mode" label="Mode">
              {(c) => (
                <Select {...c} value={mode} onChange={(e) => setMode(e.target.value as SubcontractMode)}>
                  {SUBCONTRACT_MODES.map((m) => (
                    <option key={m} value={m}>
                      {MODE_LABELS[m]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </Card>
        <Card
          title="Purchase agreements"
          actions={
            <Button size="sm" onClick={agreements.add}>
              Add agreement
            </Button>
          }
        >
          <div className={styles.stack}>
            {agreements.rows.map((a, i) => (
              <fieldset key={i} className={styles.lineRow}>
                <legend className={styles.groupsLegend}>{`Agreement ${i + 1}`}</legend>
                {field(`a-${i}-vendor`, "Vendor", a.vendorId, (v) => agreements.update(i, { vendorId: v }))}
                {field(`a-${i}-item`, "Service item", a.itemId, (v) => agreements.update(i, { itemId: v }))}
                {field(`a-${i}-price`, "Unit price", a.unitPrice, (v) => agreements.update(i, { unitPrice: v }), { decimal: true })}
                {field(`a-${i}-currency`, "Currency", a.currency, (v) => agreements.update(i, { currency: v }))}
                {field(`a-${i}-from`, "Valid from", a.validFrom, (v) => agreements.update(i, { validFrom: v }), { type: "date" })}
                {field(`a-${i}-to`, "Valid to", a.validTo, (v) => agreements.update(i, { validTo: v }), { type: "date" })}
                {agreements.rows.length > 1 ? (
                  <div className={styles.lineChecks}>
                    <Button size="sm" variant="ghost" onClick={() => agreements.remove(i)}>
                      {`Remove agreement ${i + 1}`}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
          </div>
        </Card>
        <Card
          title="Completed service jobs"
          actions={
            <Button size="sm" onClick={jobs.add}>
              Add job
            </Button>
          }
        >
          <div className={styles.stack}>
            {jobs.rows.map((j, i) => (
              <fieldset key={i} className={styles.lineRow}>
                <legend className={styles.groupsLegend}>{`Job ${i + 1}`}</legend>
                {field(`j-${i}-id`, "Kanban job", j.jobId, (v) => jobs.update(i, { jobId: v }))}
                {field(`j-${i}-vendor`, "Vendor", j.vendorId, (v) => jobs.update(i, { vendorId: v }))}
                {field(`j-${i}-item`, "Service item", j.itemId, (v) => jobs.update(i, { itemId: v }))}
                {field(`j-${i}-qty`, "Completed quantity", j.qty, (v) => jobs.update(i, { qty: v }), { decimal: true })}
                {field(`j-${i}-done`, "Completed on", j.completedOn, (v) => jobs.update(i, { completedOn: v }), { type: "date" })}
                {jobs.rows.length > 1 ? (
                  <div className={styles.lineChecks}>
                    <Button size="sm" variant="ghost" onClick={() => jobs.remove(i)}>
                      {`Remove job ${i + 1}`}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
          </div>
        </Card>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Preview PO lines
          </Button>
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="PO lines refused" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {lines ? (
        <Card title="Purchase order lines">
          <div className={styles.stack}>
            <StatGroup label="Subcontracting">
              <StatTile label="PO lines" value={lines.length} />
              <StatTile label="Jobs" value={lines.reduce((n, l) => n + l.jobIds.length, 0)} />
            </StatGroup>
            <DataTable
              label="Purchase order lines"
              columns={lineColumns}
              rows={lines}
              getRowKey={(l, i) => `${l.vendorId}-${l.itemId}-${l.unitPrice}-${i}`}
              empty={{ title: "No PO lines", description: "Add a completed service job." }}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
