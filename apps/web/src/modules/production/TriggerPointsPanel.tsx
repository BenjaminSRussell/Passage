import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  canDefineTriggers,
  createTriggerPoint,
  deleteTriggerPoint,
  describeProblem,
  listTriggerPoints,
  newIdempotencyKey,
  TRIGGER_FUNCTIONS,
  type CreateTriggerPointInput,
  type Problem,
  type ProductionOrder,
  type TriggerField,
  type TriggerFiring,
  type TriggerFunction,
  type TriggerOperation,
  type TriggerOperator,
  type TriggerPoint,
} from "./api/client";
import { dash, describeCondition, describeFiring, triggerFieldLabel, triggerFunctionLabel, triggerOperatorLabel } from "./labels";
import styles from "./pages.module.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const decimalPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;
const conditionValuePattern = /^-?[0-9]{1,12}(\.[0-9]{1,6})?$/;
/** The service's bounds: a sequence from 1 to 9999 and at most 50 operations per trigger point. */
const MAX_SEQUENCE = 9999;
export const MAX_TRIGGER_OPERATIONS = 50;

const operationDecimals = ["setupTimeH", "runTimeHPer", "scrapPct", "laborRate", "overheadRate"] as const;
type OperationDecimal = (typeof operationDecimals)[number];

const operationDecimalLabel: Record<OperationDecimal, string> = {
  setupTimeH: "Setup hours",
  runTimeHPer: "Run hours per unit",
  scrapPct: "Scrap %",
  laborRate: "Labor rate",
  overheadRate: "Overhead rate",
};

export type TriggerOperationDraft = Record<"resourceId" | OperationDecimal, string>;

export interface TriggerDraft {
  operNumber: string;
  sequence: string;
  field: TriggerField;
  operator: TriggerOperator;
  value: string;
  function: TriggerFunction;
  repeatable: boolean;
  operations: TriggerOperationDraft[];
}

export const emptyTriggerOperation = (): TriggerOperationDraft => ({
  resourceId: "",
  setupTimeH: "",
  runTimeHPer: "",
  scrapPct: "",
  laborRate: "",
  overheadRate: "",
});

export const emptyTrigger = (operNumber = ""): TriggerDraft => ({
  operNumber,
  sequence: "10",
  field: "qty_error",
  operator: ">",
  value: "0",
  function: "insert-operations",
  repeatable: false,
  operations: [emptyTriggerOperation()],
});

/** Whether a function carries operations: the ones it inserts, or the rework order's route. */
export const routesOperations = (fn: TriggerFunction) => fn === "insert-operations" || fn === "create-rework-order";

/**
 * Validates a definition like the service (FR-21.17) and builds the create body. The service
 * applies the rules that need the order: the operation is on the route and the sequence is free.
 */
export function toTriggerInput(draft: TriggerDraft): { input?: CreateTriggerPointInput; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  if (!/^[1-9][0-9]{0,8}$/.test(draft.operNumber)) {
    errors.operNumber = "Choose the operation whose feedback the trigger point evaluates.";
  }
  if (!/^[1-9][0-9]{0,3}$/.test(draft.sequence) || Number(draft.sequence) > MAX_SEQUENCE) {
    errors.sequence = `Enter a whole number from 1 to ${MAX_SEQUENCE}; trigger points on one operation act in ascending sequence.`;
  }
  if (!conditionValuePattern.test(draft.value)) {
    errors.value = "Enter a number with up to 6 decimals.";
  }
  const input: CreateTriggerPointInput = {
    operNumber: Number(draft.operNumber),
    sequence: Number(draft.sequence),
    condition: `${draft.field} ${draft.operator} ${draft.value}`,
    function: draft.function,
    repeatable: draft.repeatable,
  };
  if (routesOperations(draft.function)) {
    if (draft.operations.length === 0) {
      errors.operations = "Add at least one operation.";
    } else if (draft.operations.length > MAX_TRIGGER_OPERATIONS) {
      errors.operations = `At most ${MAX_TRIGGER_OPERATIONS} operations.`;
    }
    input.operations = draft.operations.map((op, i) => {
      if (!uuidPattern.test(op.resourceId.trim())) {
        errors[`op.${i}.resourceId`] = "Enter the resource's id (a UUID).";
      }
      const out: TriggerOperation = { resourceId: op.resourceId.trim() };
      for (const name of operationDecimals) {
        const v = op[name].trim();
        if (v === "") continue;
        if (!decimalPattern.test(v) || (name === "scrapPct" && Number(v) >= 100)) {
          errors[`op.${i}.${name}`] =
            name === "scrapPct" ? "Enter a percentage from 0 up to but excluding 100." : "Enter a non-negative number with up to 6 decimals.";
        }
        out[name] = v;
      }
      return out;
    });
  }
  return Object.keys(errors).length > 0 ? { errors } : { input, errors };
}

/** The operations of a definition in short: resource and hours of each. */
function describeOperations(tp: TriggerPoint): string {
  if (tp.operations.length === 0) {
    return dash;
  }
  return tp.operations
    .map((op) => {
      const parts = [op.resourceId.slice(0, 8)];
      if (op.setupTimeH && op.setupTimeH !== "0") parts.push(`setup ${op.setupTimeH} h`);
      if (op.runTimeHPer && op.runTimeHPer !== "0") parts.push(`${op.runTimeHPer} h per unit`);
      return parts.join(", ");
    })
    .join("; ");
}

const at = (iso: string) => <time dateTime={iso}>{iso.slice(0, 16).replace("T", " ")}</time>;

const firingColumns: DataTableColumn<TriggerFiring & { sequence: number }>[] = [
  { key: "firedAt", header: "When", mono: true, render: (f) => at(f.firedAt) },
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "sequence", header: "Sequence", align: "end" },
  { key: "function", header: "Function", render: (f) => triggerFunctionLabel[f.function] ?? f.function },
  {
    key: "feedback",
    header: "Feedback",
    render: (f) => `${f.feedback.hours} h, ${f.feedback.qtyGood} good, ${f.feedback.qtyError} error`,
  },
  { key: "status", header: "Result", render: (f) => <Badge tone={f.status === "fired" ? "success" : "warning"}>{f.status === "fired" ? "Fired" : "Failed"}</Badge> },
  { key: "outcome", header: "What happened", render: (f) => describeFiring(f) },
];

export interface TriggerPointsPanelProps {
  order: ProductionOrder;
  /** Opens another order, such as a rework order a trigger created. */
  onOpenOrder?: (id: string) => void;
}

/**
 * Trigger points of an order (FR-21.17, BR-21.13): rules on an operation evaluated when a route
 * card posts feedback on it. They insert rework operations after the operation, create a rework
 * order for the error quantity, or notify; a failure is recorded and never blocks the post.
 */
export function TriggerPointsPanel({ order, onOpenOrder }: TriggerPointsPanelProps) {
  const editable = canDefineTriggers(order.status);
  const firstOper = order.operations[0] ? String(order.operations[0].operNumber) : "";
  const [points, setPoints] = useState<{ rows: TriggerPoint[]; loading: boolean; error: Problem | null }>({ rows: [], loading: true, error: null });
  const [draft, setDraft] = useState<TriggerDraft>(() => emptyTrigger(firstOper));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState<"create" | "delete" | null>(null);
  const [confirming, setConfirming] = useState<TriggerPoint | null>(null);
  // One Idempotency-Key per definition body, so a retry after a lost response replays.
  const keys = useRef(new Map<string, string>());

  const load = useCallback(
    (signal?: AbortSignal) =>
      listTriggerPoints(order.id, signal).then(
        (rows) => setPoints({ rows, loading: false, error: null }),
        (err) => !signal?.aborted && setPoints({ rows: [], loading: false, error: describeProblem(err) }),
      ),
    [order.id],
  );

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load, order.version]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const { input, errors: found } = toTriggerInput(draft);
    setErrors(found);
    if (!input) {
      return;
    }
    const slot = JSON.stringify(input);
    const key = keys.current.get(slot) ?? newIdempotencyKey();
    keys.current.set(slot, key);
    setBusy("create");
    setFailure(null);
    setNotice("");
    try {
      const saved = await createTriggerPoint(order.id, input, key);
      setNotice(`Trigger point ${saved.sequence} on operation ${saved.operNumber} defined: ${describeCondition(saved.condition)}.`);
      setDraft({ ...emptyTrigger(draft.operNumber), sequence: String(Math.min(saved.sequence + 10, MAX_SEQUENCE)) });
      await load();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  };

  const remove = async (tp: TriggerPoint) => {
    setBusy("delete");
    setFailure(null);
    setNotice("");
    try {
      await deleteTriggerPoint(tp.id);
      setNotice(`Trigger point ${tp.sequence} on operation ${tp.operNumber} deleted.`);
      setConfirming(null);
      await load();
    } catch (err) {
      setConfirming(null);
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  };

  const columns: DataTableColumn<TriggerPoint>[] = [
    { key: "operNumber", header: "Operation", align: "end" },
    { key: "sequence", header: "Sequence", align: "end" },
    { key: "condition", header: "When", render: (tp) => describeCondition(tp.condition) },
    { key: "function", header: "Then", render: (tp) => triggerFunctionLabel[tp.function] ?? tp.function },
    { key: "operations", header: "Operations", render: describeOperations },
    { key: "repeatable", header: "Fires", render: (tp) => (tp.repeatable ? "Every time" : "Once") },
    {
      key: "firedCount",
      header: "Fired",
      align: "end",
      render: (tp) => (tp.firedCount > 0 && tp.lastFiredAt ? <>{tp.firedCount}, last {at(tp.lastFiredAt)}</> : String(tp.firedCount)),
    },
  ];
  if (editable) {
    columns.push({
      key: "delete",
      header: "Delete",
      render: (tp) =>
        tp.firedCount === 0 ? (
          <Button size="sm" variant="ghost" onClick={() => setConfirming(tp)}>
            Delete trigger point {tp.operNumber}/{tp.sequence}
          </Button>
        ) : (
          <span className={styles.hint}>Kept: it fired</span>
        ),
    });
  }

  const firings = points.rows
    .flatMap((tp) => tp.firings.map((f) => ({ ...f, sequence: tp.sequence })))
    .sort((a, b) => (a.firedAt < b.firedAt ? 1 : a.firedAt > b.firedAt ? -1 : 0));
  const reworkLinks = firings.filter((f) => f.reworkOrderId);

  const set = (patch: Partial<TriggerDraft>) => setDraft({ ...draft, ...patch });
  const setOperation = (i: number, patch: Partial<TriggerOperationDraft>) =>
    set({ operations: draft.operations.map((op, j) => (j === i ? { ...op, ...patch } : op)) });

  return (
    <Card title="Trigger points">
      <div className={styles.stack}>
        {notice ? (
          <Alert tone="success" live="status">
            {notice}
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" live="alert" title="The trigger point change was refused">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {points.error ? (
          <Alert tone="danger" title="Could not load the trigger points">
            {points.error.message} ({points.error.code})
          </Alert>
        ) : (
          <>
            <DataTable
              label="Trigger points of this order"
              columns={columns}
              rows={points.rows}
              loading={points.loading}
              getRowKey={(tp) => tp.id}
              empty={{
                title: "No trigger points",
                description: "Define a rule on an operation: when a route card reports feedback that meets it, the trigger inserts rework operations, creates a rework order or notifies.",
              }}
            />
            {firings.length > 0 ? (
              <DataTable label="Trigger firings" columns={firingColumns} rows={firings} getRowKey={(f) => f.id} />
            ) : null}
            {reworkLinks.length > 0 && onOpenOrder ? (
              <div className={styles.actions}>
                {reworkLinks.map((f) => (
                  <Button key={f.id} size="sm" variant="secondary" onClick={() => onOpenOrder(f.reworkOrderId!)}>
                    Open the rework order of operation {f.operNumber}
                  </Button>
                ))}
              </div>
            ) : null}
          </>
        )}
        {confirming ? (
          <Alert
            tone="warning"
            title={`Delete trigger point ${confirming.sequence} on operation ${confirming.operNumber}?`}
            action={
              <div className={styles.actions}>
                <Button variant="danger" loading={busy === "delete"} onClick={() => void remove(confirming)}>
                  Yes, delete it
                </Button>
                <Button variant="ghost" onClick={() => setConfirming(null)}>
                  Keep it
                </Button>
              </div>
            }
          >
            It has never fired. Feedback on the operation no longer evaluates it.
          </Alert>
        ) : null}
        {editable && order.operations.length > 0 ? (
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Define a trigger point">
            <div className={styles.fields}>
              <FormField id="tp-oper" label="On operation" error={errors.operNumber}>
                {(control) => (
                  <Select {...control} value={draft.operNumber} onChange={(e) => set({ operNumber: e.target.value })}>
                    {order.operations.map((op) => (
                      <option key={op.operNumber} value={String(op.operNumber)}>
                        {op.operNumber}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="tp-sequence" label="Sequence" error={errors.sequence}>
                {(control) => <Input {...control} inputMode="numeric" value={draft.sequence} onChange={(e) => set({ sequence: e.target.value })} />}
              </FormField>
              <FormField id="tp-field" label="When">
                {(control) => (
                  <Select {...control} value={draft.field} onChange={(e) => set({ field: e.target.value as TriggerField })}>
                    {(Object.keys(triggerFieldLabel) as TriggerField[]).map((f) => (
                      <option key={f} value={f}>
                        {triggerFieldLabel[f]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="tp-operator" label="Comparison">
                {(control) => (
                  <Select {...control} value={draft.operator} onChange={(e) => set({ operator: e.target.value as TriggerOperator })}>
                    {(Object.keys(triggerOperatorLabel) as TriggerOperator[]).map((op) => (
                      <option key={op} value={op}>
                        {triggerOperatorLabel[op]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="tp-value" label="Value" error={errors.value}>
                {(control) => <Input {...control} inputMode="decimal" value={draft.value} onChange={(e) => set({ value: e.target.value })} />}
              </FormField>
              <FormField id="tp-function" label="Then">
                {(control) => (
                  <Select
                    {...control}
                    value={draft.function}
                    onChange={(e) => {
                      const fn = e.target.value as TriggerFunction;
                      set({ function: fn, operations: routesOperations(fn) ? (draft.operations.length > 0 ? draft.operations : [emptyTriggerOperation()]) : [] });
                    }}
                  >
                    {TRIGGER_FUNCTIONS.map((fn) => (
                      <option key={fn} value={fn}>
                        {triggerFunctionLabel[fn]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="tp-repeatable" label="Fires" hint="Once per operation unless it fires on every feedback.">
                {(control) => (
                  <Select {...control} value={draft.repeatable ? "every" : "once"} onChange={(e) => set({ repeatable: e.target.value === "every" })}>
                    <option value="once">Once</option>
                    <option value="every">Every time</option>
                  </Select>
                )}
              </FormField>
            </div>
            {routesOperations(draft.function) ? (
              <div className={styles.rows}>
                <p className={styles.hint}>
                  {draft.function === "insert-operations"
                    ? "Operations inserted after the operation, numbered operation + 5, + 6 and so on, for the error quantity."
                    : "The route of the rework order, numbered 10, 20 and so on; the order is created for the error quantity."}
                </p>
                {errors.operations ? (
                  <Alert tone="danger" title="Operations">
                    {errors.operations}
                  </Alert>
                ) : null}
                {draft.operations.map((op, i) => (
                  <fieldset key={i} className={styles.row} aria-label={`Trigger operation ${i + 1}`}>
                    <legend className={styles.legend}>Operation {i + 1}</legend>
                    <FormField id={`tp-op-${i}-resource`} label="Resource" error={errors[`op.${i}.resourceId`]}>
                      {(control) => <Input {...control} value={op.resourceId} onChange={(e) => setOperation(i, { resourceId: e.target.value })} />}
                    </FormField>
                    {operationDecimals.map((name) => (
                      <FormField key={name} id={`tp-op-${i}-${name}`} label={operationDecimalLabel[name]} optional error={errors[`op.${i}.${name}`]}>
                        {(control) => <Input {...control} inputMode="decimal" value={op[name]} onChange={(e) => setOperation(i, { [name]: e.target.value })} />}
                      </FormField>
                    ))}
                    {draft.operations.length > 1 ? (
                      <Button size="sm" variant="ghost" onClick={() => set({ operations: draft.operations.filter((_, j) => j !== i) })}>
                        Remove operation {i + 1}
                      </Button>
                    ) : null}
                  </fieldset>
                ))}
                {draft.operations.length < MAX_TRIGGER_OPERATIONS ? (
                  <div className={styles.actions}>
                    <Button size="sm" variant="secondary" onClick={() => set({ operations: [...draft.operations, emptyTriggerOperation()] })}>
                      Add operation
                    </Button>
                  </div>
                ) : null}
              </div>
            ) : null}
            <p className={styles.hint}>
              A trigger point that creates rework orders also needs the duty to create rework orders. Release next and start workflow need
              scheduling and the workflow engine and are not available yet.
            </p>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "create"}>
                Define trigger point
              </Button>
            </div>
          </form>
        ) : (
          <p className={styles.hint}>
            {editable ? "This order has no operations to put a trigger point on." : "An ended or cancelled order takes no more feedback, so no trigger points."}
          </p>
        )}
      </div>
    </Card>
  );
}
