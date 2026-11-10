import { Fragment, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { ApiError, createRouteVersion, type RouteVersionInput, type RouteVersionOperation } from "./api/client";
import styles from "./pages.module.css";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

interface OperationDraft {
  key: number;
  no: string;
  operationId: string;
  setupH: string;
  runH: string;
  processQty: string;
  capability: string;
  level: string;
}

interface CreateErrors {
  form: string;
  fields: Record<string, string>;
  operations: Record<number, string>;
}

const noErrors: CreateErrors = { form: "", fields: {}, operations: {} };
const headerFields = ["productId", "siteId", "routeCode", "effectiveFrom", "effectiveTo"];
const forbiddenStatus = 403;
const operationStep = 10;

function blankOperation(key: number, no: number): OperationDraft {
  return { key, no: String(no), operationId: "", setupH: "0", runH: "0", processQty: "1", capability: "", level: "1" };
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

/** Splits a COM-0001 problem into header field messages and per-operation messages keyed by the row key. */
function createErrors(error: unknown, operations: OperationDraft[]): CreateErrors {
  if (!(error instanceof ApiError)) return { ...noErrors, form: errorText(error) };
  if (error.status === forbiddenStatus) return { ...noErrors, form: "You do not have permission to create route versions" };
  const fields: Record<string, string> = {};
  const byOperation: Record<number, string> = {};
  for (const [field, message] of Object.entries(error.errors)) {
    const match = /^operations\[(\d+)\]/.exec(field);
    const row = match ? operations[Number(match[1])] : undefined;
    if (row) byOperation[row.key] = message;
    else fields[field] = message;
  }
  const unplaced = Object.entries(fields)
    .filter(([field]) => !headerFields.includes(field))
    .map(([field, message]) => `${field} ${message}`);
  return { form: [error.message, ...unplaced].join(": "), fields, operations: byOperation };
}

/** Operations run in the order listed: each one's next operation is the following row, the last ends the route. */
export function toInput(header: Omit<RouteVersionInput, "operations">, drafts: OperationDraft[]): RouteVersionInput {
  const operations: RouteVersionOperation[] = drafts.map((draft, index) => ({
    no: Number(draft.no),
    operationId: draft.operationId.trim(),
    nextNo: index < drafts.length - 1 ? Number(drafts[index + 1].no) : 0,
    setupH: draft.setupH || "0",
    runH: draft.runH || "0",
    processQty: draft.processQty || "1",
    ...(draft.capability.trim() ? { requirements: [{ kind: "capability", refId: draft.capability.trim(), level: Number(draft.level || "0"), quantity: 1 }] } : {}),
  }));
  return { ...header, operations };
}

export function RouteVersionCreatePage({ onCreated, onCancel }: { onCreated?: (id: string) => void; onCancel?: () => void }) {
  const [productId, setProductId] = useState("");
  const [siteId, setSiteId] = useState("");
  const [routeCode, setRouteCode] = useState("");
  const [effectiveFrom, setEffectiveFrom] = useState("");
  const [effectiveTo, setEffectiveTo] = useState("");
  const [operations, setOperations] = useState<OperationDraft[]>([blankOperation(0, operationStep)]);
  const nextKey = useRef(1);
  // One key per request body: a retry of the same body after a lost response replays instead of creating a second version.
  const idempotency = useRef({ body: "", key: "" });
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<CreateErrors>(noErrors);

  function update(key: number, patch: Partial<OperationDraft>) {
    setOperations((existing) => existing.map((op) => (op.key === key ? { ...op, ...patch } : op)));
  }

  function addOperation() {
    const key = nextKey.current++;
    setOperations((existing) => {
      const last = Number(existing[existing.length - 1]?.no) || 0;
      return [...existing, blankOperation(key, last + operationStep)];
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setErrors(noErrors);
    setSubmitting(true);
    const submitted = operations;
    const input = toInput(
      { productId: productId.trim(), siteId: siteId.trim(), routeCode: routeCode.trim(), effectiveFrom, ...(effectiveTo ? { effectiveTo } : {}) },
      submitted,
    );
    const body = JSON.stringify(input);
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      const created = await createRouteVersion(input, idempotency.current.key);
      onCreated?.(created.id);
    } catch (e) {
      setErrors(createErrors(e, submitted));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <PageHeader title="New route version" description="Create a draft routing. Activate it from the route version screen once its requirements are met." />
      <Card>
        <form className={styles.form} onSubmit={submit}>
          <div className={styles.field}>
            <FormField id="new-route-product" label="Product" error={errors.fields.productId}>
              {(control) => <EntityLookup {...control} entity="product" prefill={false} value={productId} onChange={(e) => setProductId(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-route-site" label="Site" error={errors.fields.siteId}>
              {(control) => <EntityLookup {...control} entity="site" prefill={false} value={siteId} onChange={(e) => setSiteId(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-route-code" label="Route code" error={errors.fields.routeCode}>
              {(control) => <Input {...control} value={routeCode} onChange={(e) => setRouteCode(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-route-from" label="Effective from" error={errors.fields.effectiveFrom}>
              {(control) => <Input {...control} type="date" value={effectiveFrom} onChange={(e) => setEffectiveFrom(e.target.value)} required />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-route-to" label="Effective to" optional error={errors.fields.effectiveTo}>
              {(control) => <Input {...control} type="date" value={effectiveTo} onChange={(e) => setEffectiveTo(e.target.value)} />}
            </FormField>
          </div>
          {operations.map((op, index) => (
            <Fragment key={op.key}>
              <div className={styles.field}>
                <FormField id={`new-route-no-${op.key}`} label={`Operation no ${index + 1}`} error={errors.operations[op.key]}>
                  {(control) => <Input {...control} type="number" min="1" step="1" value={op.no} onChange={(e) => update(op.key, { no: e.target.value })} required />}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-route-opid-${op.key}`} label={`Operation id ${index + 1}`} optional>
                  {(control) => <Input {...control} value={op.operationId} onChange={(e) => update(op.key, { operationId: e.target.value })} maxLength={20} />}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-route-setup-${op.key}`} label={`Setup h ${index + 1}`} optional>
                  {(control) => <Input {...control} type="number" min="0" step="any" value={op.setupH} onChange={(e) => update(op.key, { setupH: e.target.value })} />}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-route-run-${op.key}`} label={`Run h ${index + 1}`} optional>
                  {(control) => <Input {...control} type="number" min="0" step="any" value={op.runH} onChange={(e) => update(op.key, { runH: e.target.value })} />}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-route-qty-${op.key}`} label={`Process qty ${index + 1}`} optional>
                  {(control) => <Input {...control} type="number" min="0" step="any" value={op.processQty} onChange={(e) => update(op.key, { processQty: e.target.value })} />}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-route-cap-${op.key}`} label={`Capability ${index + 1}`} optional>
                  {(control) => <Input {...control} value={op.capability} onChange={(e) => update(op.key, { capability: e.target.value })} />}
                </FormField>
              </div>
              <div className={styles.field}>
                <FormField id={`new-route-level-${op.key}`} label={`Capability level ${index + 1}`} optional>
                  {(control) => <Input {...control} type="number" min="0" step="1" value={op.level} onChange={(e) => update(op.key, { level: e.target.value })} />}
                </FormField>
              </div>
              {operations.length > 1 ? (
                <Button size="sm" onClick={() => setOperations((existing) => existing.filter((o) => o.key !== op.key))}>
                  Remove operation {index + 1}
                </Button>
              ) : null}
            </Fragment>
          ))}
          <div className={styles.toolbar}>
            <Button onClick={addOperation}>Add operation</Button>
            <Button type="submit" variant="primary" loading={submitting}>Create route version</Button>
            {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {errors.form || null}
        </Alert>
      </div>
    </>
  );
}
