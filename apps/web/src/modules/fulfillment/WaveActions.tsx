import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, type DataTableColumn } from "../../shell/ui";
import { assignWave, closeWave, describeProblem, type Problem, type Wave, type WaveLine } from "./api/client";
import { compareDecimal, dash, shortId } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";

/** A stale wave version gets a message that names the wave, not the shipment. */
function explainWave(problem: Problem): Problem {
  return problem.code === "conflict_version" ? { ...problem, message: "Someone else changed this wave. Reload it and try again." } : problem;
}

/** The picker name rules of the service: 1 to 64 characters, no control characters. */
export function validatePicker(value: string): string | undefined {
  const v = value.trim();
  if (v === "") return "Enter the picker's user name or badge.";
  if ([...v].length > 64) return "Use at most 64 characters.";
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(v)) return "Remove the control characters.";
  return undefined;
}

export function WaveStatusBadge({ status }: { status: Wave["status"] }) {
  return status === "CLOSED" ? <Badge tone="neutral">Closed</Badge> : <Badge tone="info">Open</Badge>;
}

/** Give the wave to a picker, or hand it to another one while it is open (FR-28.13). */
export function AssignWaveCard({ wave, onDone }: { wave: Wave; onDone(): void }) {
  const [picker, setPicker] = useState(wave.picker ?? "");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  if (wave.status !== "OPEN") return null;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePicker(picker);
    setError(found);
    setFailure(null);
    setDone(null);
    if (found) return;
    setBusy(true);
    try {
      const res = await assignWave(wave.id, wave.version, picker.trim(), key.current());
      key.renew();
      setDone(`Wave ${res.waveNumber} is assigned to ${res.picker}.`);
      onDone();
    } catch (e) {
      const problem = explainWave(describeProblem(e));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title={wave.picker ? "Reassign the wave" : "Assign the wave"}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="wave-picker" label="Picker" error={error} hint={wave.picker ? `Now assigned to ${wave.picker}.` : "Nobody is assigned yet."}>
          {(control) => (
            <Input
              {...control}
              value={picker}
              invalid={Boolean(error)}
              maxLength={80}
              onChange={(event) => {
                setPicker(event.target.value);
                key.renew();
              }}
            />
          )}
        </FormField>
        {done ? (
          <Alert tone="success" title="Wave assigned" live="status">
            {done}
          </Alert>
        ) : null}
        <ProblemAlert title="Could not assign the wave" problem={failure} />
        <div className={styles.actions}>
          <Button type="submit" disabled={busy}>
            {busy ? "Saving…" : wave.picker ? "Reassign" : "Assign"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Close the wave once every line is picked; the service answers FUL-0055 while a line is left. */
export function CloseWavePanel({ wave, onDone }: { wave: Wave; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  if (wave.status !== "OPEN") return null;
  const left = (wave.lines ?? []).filter((l) => !l.pickConfirmed && wave.shipments.find((s) => s.id === l.shipmentId)?.status !== "CANCELLED").length;

  async function close() {
    setBusy(true);
    setFailure(null);
    try {
      await closeWave(wave.id, wave.version, key.current());
      key.renew();
      setConfirming(false);
      onDone();
    } catch (e) {
      const problem = explainWave(describeProblem(e));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (!confirming) {
    return (
      <div className={styles.actions}>
        <Button variant="secondary" onClick={() => setConfirming(true)}>
          Close wave
        </Button>
        {left > 0 ? <span className={styles.note}>{left === 1 ? "1 line is" : `${left} lines are`} still to pick.</span> : null}
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <Alert
        tone="warning"
        title="Close this wave?"
        action={
          <span className={styles.actions}>
            <Button variant="danger" size="sm" loading={busy} onClick={close}>
              Yes, close it
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
              Keep it open
            </Button>
          </span>
        }
      >
        A closed wave can no longer be assigned. Every line must be picked first; shipments carry on to packing.
      </Alert>
      <ProblemAlert title="Could not close the wave" problem={failure} />
    </div>
  );
}

/** Pick path order: by zone (unzoned last), then product, then shipment. */
export function pickListOrder(lines: WaveLine[]): WaveLine[] {
  return [...lines].sort(
    (a, b) =>
      (a.zone ?? "￿").localeCompare(b.zone ?? "￿") ||
      a.productId.localeCompare(b.productId) ||
      a.shipmentNumber.localeCompare(b.shipmentNumber),
  );
}

const listColumns: DataTableColumn<WaveLine>[] = [
  { key: "zone", header: "Zone", render: (row) => row.zone ?? dash },
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "shipmentNumber", header: "Shipment", mono: true },
  { key: "qtyPlanned", header: "To pick", align: "end", render: (row) => `${row.qtyPlanned} ${row.unit}` },
  {
    key: "qtyPicked",
    header: "Picked",
    align: "end",
    render: (row) => (row.pickConfirmed ? `${row.qtyPicked} ${row.unit}${compareDecimal(row.qtyPicked, row.qtyPlanned) < 0 ? " (short)" : ""}` : dash),
  },
];

/** The printable pick list of the wave, in pick path order, headed by wave number and picker. */
export function PickListCard({ wave }: { wave: Wave }) {
  const lines = pickListOrder(wave.lines ?? []);
  return (
    <Card title="Pick list">
      <div className={styles.stack}>
        <p className={styles.note}>
          Wave {wave.waveNumber}, picker {wave.picker ?? "not assigned"}. Lines run by zone, then product.
        </p>
        <DataTable
          label="Pick list"
          columns={listColumns}
          rows={lines}
          getRowKey={(row) => row.shipmentLineId}
          empty={{ title: "No lines", description: "This wave holds no line you can see." }}
        />
        <div className={`${styles.actions} ${styles.noPrint}`}>
          <Button variant="secondary" onClick={() => window.print()}>
            Print pick list
          </Button>
        </div>
      </div>
    </Card>
  );
}
