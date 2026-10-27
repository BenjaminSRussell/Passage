import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  createWorkOrder,
  describeProblem,
  getCase,
  LINE_KINDS,
  listWorkOrders,
  newKey,
  WO_STATUSES,
  type LineKind,
  type Problem,
  type WorkOrder,
  type WorkOrderInput,
  type WorkOrderStatus,
} from "./api/client";
import { DECIMAL, duration, money, sentence, shortId, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";

const columns: DataTableColumn<WorkOrder>[] = [
  { key: "number", header: "Work order", mono: true },
  { key: "customerPartyId", header: "Customer", mono: true, render: (row) => shortId(row.customerPartyId) },
  { key: "territoryId", header: "Territory", mono: true, render: (row) => shortId(row.territoryId) },
  { key: "caseId", header: "Case", mono: true, render: (row) => shortId(row.caseId) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "estDurationMinutes", header: "Estimate", align: "end", render: (row) => duration(row.estDurationMinutes) },
  { key: "billable", header: "Billed", align: "end", render: (row) => money(row.posting?.billableTotal, row.currency) },
];

/** Service work order list (PRD 8.45.14 "Service work order list"). */
export function WorkOrdersPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<WorkOrderStatus | "">("");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Service work orders"
        description="Work orders from cases and agreements. Open one to move it along its status or post it to billing once completed."
        actions={
          <Link className={buttonClassName("primary")} to="/service/work-orders/new">
            New work order
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="work-orders-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as WorkOrderStatus | "")}>
              <option value="">Any status</option>
              {WO_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <PagedTable
        label="Service work orders"
        columns={columns}
        load={(cursor, signal) => listWorkOrders({ status, cursor }, signal)}
        loadKey={status}
        empty={{ title: "No work orders yet", description: "Create a work order from a case, or directly here." }}
        onRowOpen={(row) => navigate(`/service/work-orders/${row.id}`)}
      />
    </div>
  );
}

export interface WorkLineValues {
  kind: LineKind;
  qty: string;
  unitPrice: string;
  unitCost: string;
}

export interface WorkOrderValues {
  customerPartyId: string;
  legalEntityId: string;
  territoryId: string;
  caseId: string;
  agreementId: string;
  estDurationMinutes: string;
  currency: string;
  lines: WorkLineValues[];
}

export const emptyWorkLine = (): WorkLineValues => ({ kind: "LABOR", qty: "1", unitPrice: "0", unitCost: "0" });

type Errors = Record<string, string>;

/** Checks that mirror the service (api.go createWorkOrder). Line errors use the service's lines[i] keys. */
export function validateWorkOrder(values: WorkOrderValues): { errors: Errors; input?: WorkOrderInput } {
  const errors: Errors = {};
  for (const name of ["customerPartyId", "legalEntityId", "territoryId"] as const) {
    if (!UUID.test(values[name].trim())) errors[name] = "Enter a UUID.";
  }
  for (const name of ["caseId", "agreementId"] as const) {
    const v = values[name].trim();
    if (v && !UUID.test(v)) errors[name] = "Enter a UUID, or leave it empty.";
  }
  const minutes = Number(values.estDurationMinutes);
  if (!Number.isInteger(minutes) || minutes < 1 || minutes > 1440) errors.estDurationMinutes = "Enter whole minutes from 1 to 1440.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (values.lines.length > 200) errors.lines = "Use at most 200 lines.";
  values.lines.forEach((l, i) => {
    if (!DECIMAL.test(l.qty) || Number(l.qty) === 0) errors[`lines[${i}]`] = "Quantity must be a positive decimal.";
    else if (!DECIMAL.test(l.unitPrice) || !DECIMAL.test(l.unitCost)) errors[`lines[${i}]`] = "Unit price and unit cost must be decimals of at least 0.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  const caseId = values.caseId.trim();
  const agreementId = values.agreementId.trim();
  return {
    errors,
    input: {
      customerPartyId: values.customerPartyId.trim(),
      legalEntityId: values.legalEntityId.trim(),
      territoryId: values.territoryId.trim(),
      ...(caseId ? { caseId } : {}),
      ...(agreementId ? { agreementId } : {}),
      estDurationMinutes: minutes,
      currency: values.currency,
      lines: values.lines.map((l) => ({ kind: l.kind, qty: l.qty, unitPrice: l.unitPrice, unitCost: l.unitCost })),
    },
  };
}

/** Create a service work order (FR-45.6). From a case, the customer and legal entity come from the case and its agreement is inherited. */
export function NewWorkOrderPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const fromCase = params.get("caseId") ?? "";
  const [values, setValues] = useState<WorkOrderValues>({
    customerPartyId: "",
    legalEntityId: "",
    territoryId: "",
    caseId: fromCase,
    agreementId: "",
    estDurationMinutes: "60",
    currency: "USD",
    lines: [emptyWorkLine()],
  });
  const [caseNumber, setCaseNumber] = useState<string | null>(null);
  const [caseError, setCaseError] = useState<Problem | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    if (!fromCase) return;
    const controller = new AbortController();
    getCase(fromCase, controller.signal).then(
      (c) => {
        setCaseNumber(c.number);
        setValues((v) => ({ ...v, customerPartyId: c.customerPartyId, legalEntityId: c.legalEntityId, caseId: c.id }));
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setCaseError(describeProblem(error));
      },
    );
    return () => controller.abort();
  }, [fromCase]);

  function update(patch: Partial<WorkOrderValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  const updateLine = (i: number, patch: Partial<WorkLineValues>) => update({ lines: values.lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateWorkOrder(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createWorkOrder(input, key.current);
      navigate(`/service/work-orders/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof WorkOrderValues, "lines">, label: string, hint?: string, extra: { mono?: boolean; optional?: boolean; numeric?: boolean } = {}) => (
    <FormField id={`wo-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          inputMode={extra.numeric ? "numeric" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New service work order"
        description={caseNumber ? `For case ${caseNumber}. The work order bills under the agreement the case resolved to.` : "The work order starts unscheduled."}
      />
      {caseError ? (
        <Alert tone="danger" title="Could not load the case" live="alert">
          {caseError.message} ({caseError.code})
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New service work order">
          {text("customerPartyId", "Customer", "UUID of the customer party.", { mono: true })}
          {text("legalEntityId", "Legal entity", "UUID of the servicing legal entity.", { mono: true })}
          {text("territoryId", "Territory", "UUID of the dispatch territory.", { mono: true })}
          {text("caseId", "Case", "UUID of the case this work order resolves.", { mono: true, optional: true })}
          {text("agreementId", "Agreement", "Leave empty to use the case's agreement.", { mono: true, optional: true })}
          {text("estDurationMinutes", "Estimated minutes", undefined, { numeric: true })}
          {text("currency", "Currency")}
          <div className={`${styles.wide} ${styles.lines}`}>
            {values.lines.map((line, i) => (
              <fieldset key={i} className={styles.lineCard}>
                <legend className={styles.lineLegend}>Line {i + 1}</legend>
                <FormField id={`wline-${i}-kind`} label="Kind">
                  {(control) => (
                    <Select {...control} value={line.kind} onChange={(e) => updateLine(i, { kind: e.target.value as LineKind })}>
                      {LINE_KINDS.map((k) => (
                        <option key={k} value={k}>
                          {sentence(k)}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                <FormField id={`wline-${i}-qty`} label="Quantity" hint={line.kind === "LABOR" ? "Hours." : undefined}>
                  {(control) => <Input {...control} inputMode="decimal" value={line.qty} onChange={(e) => updateLine(i, { qty: e.target.value })} />}
                </FormField>
                <FormField id={`wline-${i}-price`} label="Unit price">
                  {(control) => <Input {...control} inputMode="decimal" value={line.unitPrice} onChange={(e) => updateLine(i, { unitPrice: e.target.value })} />}
                </FormField>
                <FormField id={`wline-${i}-cost`} label="Unit cost" hint="Recorded as warranty cost when a warranty covers the line.">
                  {(control) => <Input {...control} inputMode="decimal" value={line.unitCost} onChange={(e) => updateLine(i, { unitCost: e.target.value })} />}
                </FormField>
                {errors[`lines[${i}]`] ? (
                  <p role="alert" className={styles.fieldError}>
                    {errors[`lines[${i}]`]}
                  </p>
                ) : null}
                <div className={styles.formActions}>
                  <Button size="sm" variant="ghost" onClick={() => update({ lines: values.lines.filter((_, idx) => idx !== i) })}>
                    Remove line {i + 1}
                  </Button>
                </div>
              </fieldset>
            ))}
            {errors.lines ? (
              <p role="alert" className={styles.fieldError}>
                {errors.lines}
              </p>
            ) : null}
            <div>
              <Button onClick={() => update({ lines: [...values.lines, emptyWorkLine()] })}>Add line</Button>
            </div>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the work order" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create work order
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/work-orders">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
