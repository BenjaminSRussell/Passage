import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startOpenOrdersRun, type OpenOrdersRunRequest, type Problem } from "./api/client";
import { localToUtc, parseOpenOrders, SOURCE_LABELS, SOURCE_SYSTEMS, splitList, type SourceSystem } from "./domain";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface OpenOrdersRunValues {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoff: string;
  freeze: string;
  quarantineMaxPct: string;
  items: string;
  customers: string;
  vendors: string;
  lines: string;
}

type Field = keyof OpenOrdersRunValues;
type Errors = Partial<Record<Field, string>>;

const PCT = /^\d{1,2}(\.\d{1,4})?$/;

/** The checks the service makes before it loads an open_orders extract, and the request the form sends. */
export function validateOpenOrdersRun(values: OpenOrdersRunValues): { errors: Errors; input?: OpenOrdersRunRequest } {
  const errors: Errors = {};
  const company = values.sourceCompany.trim();
  if (company === "" || company.length > 40) errors.sourceCompany = "Enter the legacy company (DataAreaId or company code).";
  const cutoffTs = localToUtc(values.cutoff);
  const freezeTs = localToUtc(values.freeze);
  if (!cutoffTs) errors.cutoff = "Enter the extract cutoff.";
  if (!freezeTs) errors.freeze = "Enter the legacy transaction freeze.";
  if (cutoffTs && freezeTs && cutoffTs > freezeTs) errors.cutoff = "The cutoff can't be after the legacy freeze (MIG-1501).";
  const pct = values.quarantineMaxPct.trim();
  if (pct !== "" && (!PCT.test(pct) || Number(pct) > 10)) errors.quarantineMaxPct = "Enter a percentage from 0 to 10.";
  const items = splitList(values.items);
  const customers = splitList(values.customers);
  const vendors = splitList(values.vendors);
  if (items.length === 0) errors.items = "List the item master the lines are checked against.";
  const parsed = parseOpenOrders(values.lines);
  if (parsed.errors.length > 0) errors.lines = parsed.errors.slice(0, 3).join(" ");
  else if (parsed.records.length === 0) errors.lines = "Paste at least one line.";
  else {
    if (customers.length === 0 && parsed.records.some((r) => r.orderType === "SALES")) errors.customers = "List the customer master the sales order lines are checked against.";
    if (vendors.length === 0 && parsed.records.some((r) => r.orderType === "PURCHASE")) errors.vendors = "List the vendor master the purchase order lines are checked against.";
  }
  if (Object.keys(errors).length > 0 || !cutoffTs || !freezeTs) return { errors };
  return {
    errors,
    input: {
      sourceSystem: values.sourceSystem,
      sourceCompany: company,
      cutoffTs,
      freezeTs,
      ...(pct !== "" ? { quarantineMaxPct: pct } : {}),
      items,
      ...(customers.length > 0 ? { customers } : {}),
      ...(vendors.length > 0 ? { vendors } : {}),
      records: parsed.records,
    },
  };
}

/** Load legacy open sales and purchase order lines as opening lines from a pasted extract (FR-49.1, FR-49.2). */
export function NewOpenOrdersRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<OpenOrdersRunValues>({
    sourceSystem: "AX2012",
    sourceCompany: "",
    cutoff: "",
    freeze: "",
    quarantineMaxPct: "",
    items: "",
    customers: "",
    vendors: "",
    lines: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<OpenOrdersRunValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateOpenOrdersRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await startOpenOrdersRun(input, key.current);
      navigate(`/migration-factory/runs/${run.runId}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "MIG-0007") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Field, label: string, hint?: string, extra?: Record<string, unknown>, optional?: boolean) => (
    <FormField id={`oo-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<OpenOrdersRunValues>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Load open orders"
        description="Legacy sales and purchase order lines that are not fully delivered at the cutoff, loaded as opening lines and reconciled to the extract. Nothing posts to sales or purchasing here."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="oo-sourceSystem" label="Source system">
            {(control) => (
              <Select {...control} value={values.sourceSystem} onChange={(event) => update({ sourceSystem: event.target.value as SourceSystem })}>
                {SOURCE_SYSTEMS.map((s) => (
                  <option key={s} value={s}>
                    {SOURCE_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("sourceCompany", "Legacy company", "DataAreaId in AX, company code in SAP.", { maxLength: 40 })}
          {text("cutoff", "Extract cutoff (UTC)", undefined, { type: "datetime-local" })}
          {text("freeze", "Legacy freeze (UTC)", "The cutoff must not be after it.", { type: "datetime-local" })}
          {text("quarantineMaxPct", "Quarantine limit (%)", "The load stops above this share. Default 1.", { inputMode: "decimal" }, true)}
          {(["items", "customers", "vendors"] as const).map((id) => (
            <div className={styles.wide} key={id}>
              <FormField
                id={`oo-${id}`}
                label={id === "items" ? "Item master" : id === "customers" ? "Customer master" : "Vendor master"}
                hint={id === "items" ? "Item ids in Passage, separated by commas, spaces or lines." : `Needed when the extract has ${id === "customers" ? "sales" : "purchase"} order lines.`}
                optional={id !== "items"}
                error={errors[id]}
              >
                {(control) => <Textarea {...control} mono rows={2} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<OpenOrdersRunValues>)} />}
              </FormField>
            </div>
          ))}
          <div className={styles.wide}>
            <FormField
              id="oo-lines"
              label="Open order lines"
              hint="One per line: legacyId, orderType, orderNumber, lineNumber, item, party, unit, currency, orderedQty, deliveredQty, unitPrice, deliveryDate. Order type is SALES or PURCHASE; the date is optional."
              error={errors.lines}
            >
              {(control) => (
                <Textarea {...control} mono rows={10} value={values.lines} invalid={Boolean(errors.lines)} onChange={(event) => update({ lines: event.target.value })} />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not load the orders" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run open_orders
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration-factory">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
