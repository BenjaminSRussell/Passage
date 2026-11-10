import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  assignAsBuilt,
  describeProblem,
  detachAsBuilt,
  listOrderAsBuilt,
  newIdempotencyKey,
  type AsBuiltRecord,
  type AssignAsBuiltInput,
  type Problem,
  type ProductionOrder,
  type Tracking,
} from "./api/client";
import { trackingLabel } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const quantityPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;

/** A serial, batch or reference number: 1 to 100 characters without surrounding spaces (as the service checks). */
export const validTrackingId = (v: string) => v !== "" && v.trim() === v && v.length <= 100;

export interface AsBuiltDraft {
  finishedSerialOrBatch: string;
  componentItemId: string;
  tracking: Tracking;
  componentSerialOrBatch: string;
  qty: string;
}

export const emptyAsBuilt = (componentItemId = ""): AsBuiltDraft => ({
  finishedSerialOrBatch: "",
  componentItemId,
  tracking: "serial",
  componentSerialOrBatch: "",
  qty: "",
});

/** Validates the assignment like the service and builds the AssignAsBuilt body. */
export function toAsBuiltInput(draft: AsBuiltDraft): { input?: AssignAsBuiltInput; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  if (!validTrackingId(draft.finishedSerialOrBatch)) {
    errors.finishedSerialOrBatch = "Enter the finished serial or batch number (up to 100 characters, no surrounding spaces).";
  }
  if (draft.componentItemId === "") {
    errors.componentItemId = "Choose the component.";
  }
  if (!validTrackingId(draft.componentSerialOrBatch)) {
    errors.componentSerialOrBatch = "Enter the component serial or batch number (up to 100 characters, no surrounding spaces).";
  }
  const input: AssignAsBuiltInput = {
    finishedSerialOrBatch: draft.finishedSerialOrBatch,
    componentItemId: draft.componentItemId,
    tracking: draft.tracking,
    componentSerialOrBatch: draft.componentSerialOrBatch,
  };
  if (draft.tracking === "batch") {
    if (!quantityPattern.test(draft.qty) || !/[1-9]/.test(draft.qty)) {
      errors.qty = "Enter the positive batch quantity with up to 6 decimals.";
    }
    input.qty = draft.qty;
  }
  return Object.keys(errors).length > 0 ? { errors } : { input, errors };
}

export interface AsBuiltPanelProps {
  order: ProductionOrder;
}

/**
 * As-built genealogy of an order (FR-21.19, BR-21.14): which component serials or batches went
 * into each finished serial or batch. Assignments and corrections are allowed while the order is
 * Started or Reported as finished; a component serial belongs to one finished serial only.
 */
export function AsBuiltPanel({ order }: AsBuiltPanelProps) {
  const components = Array.from(new Map(order.lines.map((l) => [l.itemId, l])).values());
  const editable = order.status === "Started" || order.status === "ReportedAsFinished";
  const [records, setRecords] = useState<{ rows: AsBuiltRecord[]; loading: boolean; error: Problem | null }>({ rows: [], loading: true, error: null });
  const [draft, setDraft] = useState<AsBuiltDraft>(() => emptyAsBuilt(components[0]?.itemId));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState<"assign" | "detach" | null>(null);
  const [confirming, setConfirming] = useState<AsBuiltRecord | null>(null);
  // One Idempotency-Key per assignment body, so a retry after a lost response replays.
  const keys = useRef(new Map<string, string>());

  const load = useCallback(
    (signal?: AbortSignal) =>
      listOrderAsBuilt(order.id, signal).then(
        (rows) => setRecords({ rows, loading: false, error: null }),
        (err) => !signal?.aborted && setRecords({ rows: [], loading: false, error: describeProblem(err) }),
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
    const { input, errors: found } = toAsBuiltInput(draft);
    setErrors(found);
    if (!input) {
      return;
    }
    const slot = JSON.stringify(input);
    const key = keys.current.get(slot) ?? newIdempotencyKey();
    keys.current.set(slot, key);
    setBusy("assign");
    setFailure(null);
    setNotice("");
    try {
      const saved = await assignAsBuilt(order.id, input, key);
      setNotice(`${saved.componentSerialOrBatch} assigned to ${saved.finishedSerialOrBatch}.`);
      setDraft({ ...emptyAsBuilt(draft.componentItemId), finishedSerialOrBatch: draft.finishedSerialOrBatch, tracking: draft.tracking });
      await load();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  };

  const detach = async (record: AsBuiltRecord) => {
    setBusy("detach");
    setFailure(null);
    setNotice("");
    try {
      await detachAsBuilt(record.id);
      setNotice(`${record.componentSerialOrBatch} detached from ${record.finishedSerialOrBatch}.`);
      setConfirming(null);
      await load();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  };

  const columns: DataTableColumn<AsBuiltRecord>[] = [
    { key: "finishedSerialOrBatch", header: "Finished serial or batch", mono: true },
    { key: "componentItemId", header: "Component", mono: true },
    { key: "tracking", header: "Tracking", render: (r) => trackingLabel[r.tracking] ?? r.tracking },
    { key: "componentSerialOrBatch", header: "Component serial or batch", mono: true },
    { key: "qty", header: "Quantity", align: "end" },
    { key: "assignedBy", header: "Assigned by" },
    { key: "assignedAt", header: "Assigned", mono: true, render: (r) => <time dateTime={r.assignedAt}>{r.assignedAt.slice(0, 16).replace("T", " ")}</time> },
  ];
  if (editable) {
    columns.push({
      key: "detach",
      header: "Correction",
      render: (r) => (
        <Button size="sm" variant="ghost" onClick={() => setConfirming(r)}>
          Detach {r.componentSerialOrBatch}
        </Button>
      ),
    });
  }

  const set = (patch: Partial<AsBuiltDraft>) => setDraft({ ...draft, ...patch });

  return (
    <Card title="As-built">
      <div className={styles.stack}>
        {notice ? (
          <Alert tone="success" live="status">
            {notice}
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" live="alert" title="The as-built change was refused">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        {records.error ? (
          <Alert tone="danger" title="Could not load the as-built records">
            <ProblemMessage problem={records.error} />
          </Alert>
        ) : (
          <DataTable
            label="As-built records"
            columns={columns}
            rows={records.rows}
            loading={records.loading}
            getRowKey={(r) => r.id}
            empty={{
              title: "No as-built records",
              description: "Component serials and batches assigned to the finished serials of this order appear here.",
            }}
          />
        )}
        {confirming ? (
          <Alert
            tone="warning"
            title={`Detach ${confirming.componentSerialOrBatch} from ${confirming.finishedSerialOrBatch}?`}
            action={
              <div className={styles.actions}>
                <Button variant="danger" loading={busy === "detach"} onClick={() => void detach(confirming)}>
                  Yes, detach it
                </Button>
                <Button variant="ghost" onClick={() => setConfirming(null)}>
                  Keep it
                </Button>
              </div>
            }
          >
            The record stays in the audit trail and the component serial can be assigned again.
          </Alert>
        ) : null}
        {editable && components.length > 0 ? (
          <form className={styles.stack} onSubmit={submit} noValidate aria-label="Assign as-built">
            <div className={styles.fields}>
              <FormField id="ab-finished" label="Finished serial or batch" error={errors.finishedSerialOrBatch}>
                {(control) => <Input {...control} value={draft.finishedSerialOrBatch} onChange={(e) => set({ finishedSerialOrBatch: e.target.value })} />}
              </FormField>
              <FormField id="ab-component" label="Component" error={errors.componentItemId}>
                {(control) => (
                  <Select {...control} value={draft.componentItemId} onChange={(e) => set({ componentItemId: e.target.value })}>
                    {components.map((l) => (
                      <option key={l.itemId} value={l.itemId}>
                        {l.lineNo} · {l.itemId}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="ab-tracking" label="Tracking">
                {(control) => (
                  <Select {...control} value={draft.tracking} onChange={(e) => set({ tracking: e.target.value as Tracking, qty: "" })}>
                    <option value="serial">{trackingLabel.serial}</option>
                    <option value="batch">{trackingLabel.batch}</option>
                  </Select>
                )}
              </FormField>
              <FormField id="ab-component-serial" label="Component serial or batch" error={errors.componentSerialOrBatch}>
                {(control) => <Input {...control} value={draft.componentSerialOrBatch} onChange={(e) => set({ componentSerialOrBatch: e.target.value })} />}
              </FormField>
              {draft.tracking === "batch" ? (
                <FormField id="ab-qty" label="Batch quantity" error={errors.qty}>
                  {(control) => <Input {...control} inputMode="decimal" value={draft.qty} onChange={(e) => set({ qty: e.target.value })} />}
                </FormField>
              ) : null}
            </div>
            <p className={styles.hint}>
              A serial counts as 1. The assigned quantity of a component cannot exceed what the order consumed of it.
            </p>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "assign"}>
                Assign component
              </Button>
            </div>
          </form>
        ) : (
          <p className={styles.hint}>
            {editable
              ? "This order has no components to assign."
              : "Components are assigned to finished serials while the order is Started or Reported as finished."}
          </p>
        )}
      </div>
    </Card>
  );
}
