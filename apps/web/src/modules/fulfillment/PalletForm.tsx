// Nested containers: put CLOSED cartons on a pallet with its own SSCC, and dissolve
// a pallet before it ships. Each command keeps one Idempotency-Key until its values
// change and sends the shipment version as If-Match.
import { useState, type FormEvent } from "react";
import { Button, Card, FormField, Input } from "../../shell/ui";
import { describeProblem, nestContainers, unnestContainer, type Container, type Problem, type Shipment } from "./api/client";
import { explain, ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";

const NESTABLE: Shipment["status"][] = ["PICKED", "PACKED"];

/** A carton that can go on a pallet: CLOSED, free, and not a pallet itself. */
export function isFreeCarton(container: Container): boolean {
  return container.status === "CLOSED" && !container.parentContainerId && (container.childContainerIds ?? []).length === 0;
}

/** A CLOSED pallet that holds cartons can be dissolved while the shipment has not posted them. */
export function canUnnest(shipment: Shipment, container: Container): boolean {
  return NESTABLE.includes(shipment.status) && container.status === "CLOSED" && (container.childContainerIds ?? []).length > 0;
}

/** Pick the cartons and the pallet type; the service issues the pallet SSCC. */
export function NestForm({ shipment, onDone }: { shipment: Shipment; onDone(next: Shipment): void }) {
  const cartons = shipment.containers.filter(isFreeCarton);
  const [containerType, setContainerType] = useState("");
  const [chosen, setChosen] = useState<string[]>([]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (containerType.trim() === "" || containerType.trim().length > 64) found.containerType = "Enter the pallet type, such as EUR-PALLET.";
    if (chosen.length === 0) found.cartons = "Choose at least one carton.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const res = await nestContainers(shipment.id, shipment.version, { containerType: containerType.trim(), containerIds: chosen }, key.current());
      key.renew();
      setNotice(`Pallet closed with SSCC ${res.container.sscc}.`);
      onDone(res.shipment);
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (!NESTABLE.includes(shipment.status) || cartons.length === 0) return null;
  return (
    <Card title="Put cartons on a pallet">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="nest-container-type" label="Pallet type" hint="The pallet gets its own SSCC; the cartons keep theirs and ship with it." error={errors.containerType}>
          {(control) => (
            <Input
              {...control}
              maxLength={64}
              value={containerType}
              invalid={Boolean(errors.containerType)}
              onChange={(event) => {
                setContainerType(event.target.value);
                key.renew();
              }}
            />
          )}
        </FormField>
        <fieldset className={styles.fieldset}>
          <legend>Cartons</legend>
          {cartons.map((c) => (
            <label key={c.id} className={styles.checkRow}>
              <input
                type="checkbox"
                aria-label={`Carton ${c.sscc}`}
                checked={chosen.includes(c.id)}
                onChange={(event) => {
                  setChosen(event.target.checked ? [...chosen, c.id] : chosen.filter((id) => id !== c.id));
                  key.renew();
                }}
              />
              <span className={styles.mono}>{c.sscc}</span> {c.containerType}, {c.grossKg} kg
            </label>
          ))}
          {errors.cartons ? (
            <p role="alert" className={styles.note}>
              {errors.cartons}
            </p>
          ) : null}
        </fieldset>
        <ProblemAlert title="Could not nest the containers" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Close pallet
          </Button>
          {notice ? (
            <p role="status" className={styles.note}>
              {notice}
            </p>
          ) : null}
        </div>
      </form>
    </Card>
  );
}

/** Dissolve button with an inline confirmation; the cartons stay packed. */
export function UnnestButton({ shipment, container, onDone }: { shipment: Shipment; container: Container; onDone(): void }) {
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function unnest() {
    setBusy(true);
    setFailure(null);
    try {
      await unnestContainer(shipment.id, container.id, shipment.version, key.current());
      key.renew();
      onDone();
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (!confirming) {
    return (
      <Button size="sm" variant="ghost" onClick={() => setConfirming(true)} aria-label={`Dissolve pallet ${container.sscc}`}>
        Dissolve pallet
      </Button>
    );
  }
  return (
    <span className={styles.stack}>
      <span className={styles.actions}>
        <Button size="sm" variant="danger" loading={busy} onClick={unnest} aria-label={`Confirm dissolve ${container.sscc}`}>
          Dissolve it
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>
          Keep it
        </Button>
      </span>
      <ProblemAlert title="Could not dissolve the pallet" problem={failure} />
    </span>
  );
}
