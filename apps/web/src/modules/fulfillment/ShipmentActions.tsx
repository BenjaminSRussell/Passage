// The shipment commands of the detail page: confirm pick (FR-28.2), auto-pack by
// packing rule (FR-28.15), pack a container (FR-28.3) and post the packing slip
// (FR-28.4). Each form keeps one
// Idempotency-Key until its values change, and sends the shipment version as If-Match.
import { useState, type FormEvent } from "react";
import { Button, Card, DataTable, FormField, Input, type DataTableColumn } from "../../shell/ui";
import {
  confirmPick,
  autoPack,
  describeProblem,
  packContainer,
  postPackingSlip,
  type AutoPackResult,
  type PackingSlip,
  type Problem,
  type Shipment,
  type ShipmentLine,
} from "./api/client";
import { compareDecimal, COUNTRY, CURRENCY, DECIMAL, multiplyDecimal, shortId, subtractDecimal, sumDecimals, today } from "./format";
import { explain, ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";

function lineLabel(line: ShipmentLine): string {
  return `Line ${shortId(line.orderLineId)} (${line.unit})`;
}

/** Confirm picked quantities for the lines not yet confirmed. A short pick leaves the rest on backorder. */
export function PickForm({ shipment, onDone }: { shipment: Shipment; onDone(next: Shipment): void }) {
  const open = shipment.lines.filter((l) => !l.pickConfirmed);
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(open.map((l) => [l.id, l.qtyPlanned])));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    for (const l of open) {
      const v = (values[l.id] ?? "").trim();
      if (!DECIMAL.test(v)) found[l.id] = "Enter the picked quantity, 0 or more, with at most 6 decimals.";
      else if (compareDecimal(v, l.qtyPlanned) > 0) found[l.id] = `Over-pick: at most ${l.qtyPlanned} is planned (FUL-0004).`;
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const next = await confirmPick(shipment.id, shipment.version, open.map((l) => ({ shipmentLineId: l.id, picked: values[l.id].trim() })), key.current());
      key.renew();
      onDone(next);
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (open.length === 0) return null;
  return (
    <Card title="Confirm pick">
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>Enter what was picked. Less than planned is a short pick; the remainder goes to backorder and its reservation is released.</p>
        {open.map((l) => (
          <FormField key={l.id} id={`pick-${l.id}`} label={`${lineLabel(l)} picked`} hint={`Planned ${l.qtyPlanned}`} error={errors[l.id]}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values[l.id] ?? ""}
                invalid={Boolean(errors[l.id])}
                onChange={(event) => {
                  setValues({ ...values, [l.id]: event.target.value });
                  key.renew();
                }}
              />
            )}
          </FormField>
        ))}
        <ProblemAlert title="Could not confirm the pick" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Confirm pick
          </Button>
        </div>
      </form>
    </Card>
  );
}

/**
 * Auto-pack the picked, unpacked lines by packing rule (BR-28.11). Unit weights are
 * optional: with one, a container also closes at the rule's max weight. Lines
 * without a rule stay unpacked; the page shows the FUL-0012 warnings.
 */
export function AutoPackForm({ shipment, onDone }: { shipment: Shipment; onDone(result: AutoPackResult): void }) {
  const open = shipment.lines.filter((l) => compareDecimal(l.qtyPicked, l.qtyPacked) > 0);
  const [weights, setWeights] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const unitWeights: Record<string, string> = {};
    for (const l of open) {
      const v = (weights[l.id] ?? "").trim();
      if (v === "") continue;
      if (!DECIMAL.test(v)) found[l.id] = "Enter the unit weight in kg, with at most 6 decimals, or leave it empty.";
      else unitWeights[l.id] = v;
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const result = await autoPack(shipment.id, shipment.version, unitWeights, key.current());
      key.renew();
      onDone(result);
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (open.length === 0) return null;
  return (
    <Card title="Auto-pack by packing rule">
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          Fills containers from the packing rules of each product, most specific rule first, and closes each one with an SSCC. Give a unit weight to also respect the rule&apos;s max weight.
        </p>
        {open.map((l) => (
          <FormField key={l.id} id={`autopack-${l.id}`} label={`${lineLabel(l)} unit weight (kg)`} hint={`To pack: ${subtractDecimal(l.qtyPicked, l.qtyPacked)}`} error={errors[l.id]} optional>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={weights[l.id] ?? ""}
                invalid={Boolean(errors[l.id])}
                onChange={(event) => {
                  setWeights({ ...weights, [l.id]: event.target.value });
                  key.renew();
                }}
              />
            )}
          </FormField>
        ))}
        <ProblemAlert title="Could not auto-pack" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Auto-pack
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Pack picked, unpacked quantities into one container; the service issues its SSCC. */
export function PackForm({ shipment, onDone }: { shipment: Shipment; onDone(next: Shipment): void }) {
  const open = shipment.lines.filter((l) => compareDecimal(l.qtyPicked, l.qtyPacked) > 0);
  const remaining = (l: ShipmentLine) => subtractDecimal(l.qtyPicked, l.qtyPacked);
  const [containerType, setContainerType] = useState("");
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(open.map((l) => [l.id, remaining(l)])));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (containerType.trim() === "" || containerType.trim().length > 64) found.containerType = "Enter the container type, such as CARTON-C12 or PALLET.";
    const lines: { shipmentLineId: string; qty: string }[] = [];
    for (const l of open) {
      const v = (values[l.id] ?? "").trim();
      if (v === "" || /^0+(\.0+)?$/.test(v)) continue;
      if (!DECIMAL.test(v)) found[l.id] = "Enter a quantity with at most 6 decimals.";
      else if (compareDecimal(v, remaining(l)) > 0) found[l.id] = `Only ${remaining(l)} is picked and not yet packed (FUL-0023).`;
      else lines.push({ shipmentLineId: l.id, qty: v });
    }
    if (lines.length === 0 && Object.keys(found).length === 0) found.containerType = "Enter a quantity for at least one line.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const res = await packContainer(shipment.id, shipment.version, { containerType: containerType.trim(), lines }, key.current());
      key.renew();
      setNotice(`Container closed with SSCC ${res.container.sscc}.`);
      onDone(res.shipment);
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (open.length === 0) return null;
  return (
    <Card title="Pack a container">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="pack-container-type" label="Container type" hint="The carton or pallet the lines go into." error={errors.containerType}>
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
        {open.map((l) => (
          <FormField key={l.id} id={`pack-${l.id}`} label={`${lineLabel(l)} to pack`} hint={`Picked, not packed: ${remaining(l)}`} error={errors[l.id]}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values[l.id] ?? ""}
                invalid={Boolean(errors[l.id])}
                onChange={(event) => {
                  setValues({ ...values, [l.id]: event.target.value });
                  key.renew();
                }}
              />
            )}
          </FormField>
        ))}
        <ProblemAlert title="Could not pack the container" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Close container
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

interface CostValues {
  unitCost: string;
  costSnapshotId: string;
}

interface PreviewRow {
  account: string;
  description: string;
  debit: string;
  credit: string;
}

const previewColumns: DataTableColumn<PreviewRow>[] = [
  { key: "account", header: "Account", mono: true },
  { key: "description", header: "Description" },
  { key: "debit", header: "Debit", align: "end" },
  { key: "credit", header: "Credit", align: "end" },
];

/** Post the packing slip of a PACKED shipment, with a journal preview before confirming (section 5.28.6). */
export function PostForm({ shipment, onPosted }: { shipment: Shipment; onPosted(slip: PackingSlip): void }) {
  const open = shipment.lines.filter((l) => compareDecimal(l.qtyPacked, l.qtyShipped) > 0);
  const toShip = (l: ShipmentLine) => subtractDecimal(l.qtyPacked, l.qtyShipped);
  const [postingDate, setPostingDate] = useState(today());
  const [currency, setCurrency] = useState("USD");
  const [cogsAtShipment, setCogsAtShipment] = useState(false);
  const [countryCode, setCountryCode] = useState("");
  const [costs, setCosts] = useState<Record<string, CostValues>>(() => Object.fromEntries(open.map((l) => [l.id, { unitCost: "", costSnapshotId: "" }])));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function setCost(id: string, patch: Partial<CostValues>) {
    setCosts({ ...costs, [id]: { ...costs[id], ...patch } });
    key.renew();
  }

  const priced = open.filter((l) => DECIMAL.test((costs[l.id]?.unitCost ?? "").trim()));
  const amounts = priced.map((l) => multiplyDecimal(toShip(l), costs[l.id].unitCost.trim(), 2));
  const total = sumDecimals(amounts, 2);
  const preview: PreviewRow[] =
    priced.length === open.length && open.length > 0
      ? [
          { account: cogsAtShipment ? "5000" : "1405", description: cogsAtShipment ? "Cost of goods sold" : "Delivered not invoiced", debit: `${total} ${currency}`, credit: "" },
          { account: "1400", description: "Inventory", debit: "", credit: `${total} ${currency}` },
        ]
      : [];

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!/^\d{4}-\d{2}-\d{2}$/.test(postingDate)) found.postingDate = "Enter the posting date.";
    if (!CURRENCY.test(currency.trim())) found.currency = "Enter a 3-letter currency code, such as USD.";
    if (countryCode.trim() !== "" && !COUNTRY.test(countryCode.trim())) found.countryCode = "Enter a 2-letter country code, such as DE, or leave it empty.";
    for (const l of open) {
      const c = costs[l.id] ?? { unitCost: "", costSnapshotId: "" };
      if (!DECIMAL.test(c.unitCost.trim())) found[`${l.id}-cost`] = "Enter the unit cost, 0 or more, with at most 6 decimals.";
      if (c.costSnapshotId.trim() === "" || c.costSnapshotId.trim().length > 128) found[`${l.id}-snapshot`] = "Enter the cost snapshot id (wave 20).";
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const slip = await postPackingSlip(
        shipment.id,
        shipment.version,
        {
          postingDate,
          currency: currency.trim(),
          cogsAtShipment,
          ...(countryCode.trim() ? { countryCode: countryCode.trim() } : {}),
          costs: open.map((l) => ({ shipmentLineId: l.id, unitCost: costs[l.id].unitCost.trim(), costSnapshotId: costs[l.id].costSnapshotId.trim() })),
        },
        key.current(),
      );
      key.renew();
      onPosted(slip);
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (open.length === 0) return null;
  return (
    <Card title="Post packing slip">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="post-date" label="Posting date" error={errors.postingDate}>
          {(control) => <Input {...control} type="date" value={postingDate} invalid={Boolean(errors.postingDate)} onChange={(event) => { setPostingDate(event.target.value); key.renew(); }} />}
        </FormField>
        <FormField id="post-currency" label="Currency" error={errors.currency}>
          {(control) => <Input {...control} maxLength={3} value={currency} invalid={Boolean(errors.currency)} onChange={(event) => { setCurrency(event.target.value.toUpperCase()); key.renew(); }} />}
        </FormField>
        <FormField id="post-country" label="Ship-to country" hint="Applies that country's packaging fee rates (statistical, not journaled). Empty: no packaging fee." error={errors.countryCode} optional>
          {(control) => <Input {...control} maxLength={2} value={countryCode} invalid={Boolean(errors.countryCode)} onChange={(event) => { setCountryCode(event.target.value.toUpperCase()); key.renew(); }} />}
        </FormField>
        <FormField id="post-cogs" label="Post cost of goods sold at shipment" hint="Off: Dr 1405 delivered-not-invoiced (default). On: Dr 5000 COGS.">
          {(control) => <Input {...control} type="checkbox" checked={cogsAtShipment} onChange={(event) => { setCogsAtShipment(event.target.checked); key.renew(); }} />}
        </FormField>
        {open.map((l) => (
          <fieldset key={l.id} className={styles.valueGroup}>
            <legend className={styles.legend}>{`${lineLabel(l)}: ship ${toShip(l)}`}</legend>
            <FormField id={`post-${l.id}-cost`} label="Unit cost" error={errors[`${l.id}-cost`]}>
              {(control) => <Input {...control} inputMode="decimal" value={costs[l.id]?.unitCost ?? ""} invalid={Boolean(errors[`${l.id}-cost`])} onChange={(event) => setCost(l.id, { unitCost: event.target.value })} />}
            </FormField>
            <FormField id={`post-${l.id}-snapshot`} label="Cost snapshot id" error={errors[`${l.id}-snapshot`]}>
              {(control) => <Input {...control} maxLength={128} value={costs[l.id]?.costSnapshotId ?? ""} invalid={Boolean(errors[`${l.id}-snapshot`])} onChange={(event) => setCost(l.id, { costSnapshotId: event.target.value })} />}
            </FormField>
          </fieldset>
        ))}
        <div className={styles.formActions}>
          <DataTable
            label="Journal preview"
            columns={previewColumns}
            rows={preview}
            getRowKey={(row) => row.account}
            empty={{ title: "Enter every unit cost to see the journal", description: "The slip posts one debit and one credit per line at its cost." }}
          />
        </div>
        <ProblemAlert title="Could not post the packing slip" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Post packing slip
          </Button>
        </div>
      </form>
    </Card>
  );
}
