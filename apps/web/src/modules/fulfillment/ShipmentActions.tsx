// The shipment commands of the detail page: confirm pick (FR-28.2), auto-pack by
// packing rule (FR-28.15), pack a container (FR-28.3) and post the packing slip
// (FR-28.4). Each form keeps one
// Idempotency-Key until its values change, and sends the shipment version as If-Match.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  confirmPick,
  autoPack,
  describeProblem,
  getLegalEntitySetting,
  getOperationRule,
  listDeliveryTolerances,
  packContainer,
  postPackingSlip,
  type AutoPackResult,
  type OperationRule,
  type PackingSlip,
  type Problem,
  type Shipment,
  type ShipmentLine,
  type ShortPickReason,
} from "./api/client";
import { compareDecimal, COUNTRY, CURRENCY, DECIMAL, multiplyDecimal, pickCeiling, resolveTolerance, shortId, SHORT_PICK_REASONS, subtractDecimal, sumDecimals, today } from "./format";
import { explain, ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";

function lineLabel(line: ShipmentLine): string {
  return `Line ${shortId(line.orderLineId)} (${line.unit})`;
}

/**
 * Confirm picked quantities for the lines not yet confirmed. A short pick leaves the rest on
 * backorder. On a partially shipped shipment it re-picks the lines with a remainder: the
 * picked quantity is the line's new total, at least what is already packed (FR-28.8).
 */
export function PickForm({ shipment, onDone }: { shipment: Shipment; onDone(next: Shipment): void }) {
  const remainder = shipment.status === "PARTIALLY_SHIPPED";
  const open = shipment.lines.filter((l) => (remainder ? compareDecimal(l.qtyPicked, l.qtyPlanned) < 0 : !l.pickConfirmed));
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(open.map((l) => [l.id, l.qtyPlanned])));
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();
  const isShort = (l: ShipmentLine) => {
    const v = (values[l.id] ?? "").trim();
    return DECIMAL.test(v) && compareDecimal(v, l.qtyPlanned) < 0;
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    // A quantity above plan is allowed up to the over-delivery tolerance (BR-28.7); the
    // tolerances are read only when one is entered.
    const over = open.filter((l) => DECIMAL.test((values[l.id] ?? "").trim()) && compareDecimal(values[l.id].trim(), l.qtyPlanned) > 0);
    // null: the list could not be read (for example without fulfillment.warehouse.view); the service then decides alone.
    let tolerances: Awaited<ReturnType<typeof listDeliveryTolerances>> | null = [];
    if (over.length > 0) {
      setBusy(true);
      try {
        tolerances = await listDeliveryTolerances();
      } catch {
        tolerances = null;
      }
      setBusy(false);
    }
    for (const l of open) {
      const v = (values[l.id] ?? "").trim();
      if (!DECIMAL.test(v)) found[l.id] = "Enter the picked quantity, 0 or more, with at most 6 decimals.";
      else if (compareDecimal(v, l.qtyPlanned) > 0) {
        if (tolerances === null) continue;
        const pct = resolveTolerance(tolerances, l.productId, shipment.customerId);
        const ceiling = pickCeiling(l.qtyPlanned, pct);
        if (compareDecimal(pct, "0") === 0) found[l.id] = `Over-pick: at most ${l.qtyPlanned} is planned and no over-delivery tolerance applies (FUL-0004).`;
        else if (compareDecimal(v, ceiling) > 0) found[l.id] = `Over-ship: at most ${ceiling} is allowed, ${l.qtyPlanned} planned plus a ${pct}% tolerance (FUL-0005).`;
      } else if (compareDecimal(v, l.qtyPacked) < 0) found[l.id] = `${l.qtyPacked} is already packed or shipped; enter the line's total picked, at least that (FUL-0031).`;
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const picks = open.map((l) => {
        const reason = isShort(l) ? reasons[l.id] : "";
        return { shipmentLineId: l.id, picked: values[l.id].trim(), ...(reason ? { reason: reason as ShortPickReason } : {}) };
      });
      const next = await confirmPick(shipment.id, shipment.version, picks, key.current());
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
    <Card title={remainder ? "Pick the remainder" : "Confirm pick"}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          {remainder
            ? "Enter each line's total picked so far, including what already shipped. The new quantity is packed and posted on another packing slip."
            : "Enter what was picked. Less than planned is a short pick; the remainder goes to backorder and its reservation is released."}
        </p>
        {open.map((l) => (
          <div key={l.id} className={styles.form}>
            <FormField
              id={`pick-${l.id}`}
              label={`${lineLabel(l)} picked`}
              hint={remainder ? `Planned ${l.qtyPlanned}, shipped ${l.qtyShipped}` : `Planned ${l.qtyPlanned}`}
              error={errors[l.id]}
            >
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
            {isShort(l) ? (
              <FormField id={`pick-reason-${l.id}`} label={`${lineLabel(l)} short pick reason`} optional>
                {(control) => (
                  <Select
                    {...control}
                    value={reasons[l.id] ?? ""}
                    onChange={(event) => {
                      setReasons({ ...reasons, [l.id]: event.target.value });
                      key.renew();
                    }}
                  >
                    <option value="">No reason given</option>
                    {Object.entries(SHORT_PICK_REASONS).map(([code, label]) => (
                      <option key={code} value={code}>
                        {label}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            ) : null}
          </div>
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

/** The warehouse's operation rule (BR-28.3, FR-28.15); null while loading or when it cannot be read, which leaves the form as it was. */
function useOperationRule(warehouseId: string): OperationRule | null {
  const [rule, setRule] = useState<OperationRule | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    getOperationRule(warehouseId, controller.signal).then(setRule, () => undefined);
    return () => controller.abort();
  }, [warehouseId]);
  return rule;
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
  // auto_pack_enabled of the warehouse's operation rule: off keeps packing manual (FUL-0075).
  const rule = useOperationRule(shipment.warehouseId);

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
        {rule && !rule.autoPackEnabled ? (
          <p className={styles.note} role="status">
            Auto-pack is turned off for this warehouse (see Posting and packing rules). Pack the containers by hand.
          </p>
        ) : null}
        <ProblemAlert title="Could not auto-pack" problem={failure} />
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={rule ? !rule.autoPackEnabled : false}>
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
  const [countryCode, setCountryCode] = useState("");
  const [asnEnabled, setAsnEnabled] = useState(false);
  // cogs_at_shipment is the legal entity's COGS posting policy (BR-28.1): the form shows it and previews the
  // journal with it but never sends it, so the post books per the saved policy (a named contrary value is
  // FUL-0059). asn_enabled presets the ASN checkbox once it loads; a value the poster already changed stays.
  const [cogsPolicy, setCogsPolicy] = useState<boolean | null>(null);
  const asnTouched = useRef(false);
  useEffect(() => {
    const controller = new AbortController();
    getLegalEntitySetting(shipment.legalEntityId, controller.signal).then(
      (setting) => {
        setCogsPolicy(setting.cogsAtShipment);
        if (!asnTouched.current) setAsnEnabled(setting.asnEnabled);
      },
      () => undefined,
    );
    return () => controller.abort();
  }, [shipment.legalEntityId]);
  // posting_date_source of the warehouse (BR-28.3): MANUAL asks for a date, TODAY takes the day of the post in the
  // warehouse time zone unless the poster names another, SHIP_TIMESTAMP always takes that day. A rule that cannot be
  // read leaves the form asking for a date, as before the rule existed.
  const operationRule = useOperationRule(shipment.warehouseId);
  const dateSource = operationRule?.postingDateSource ?? "MANUAL";
  const manualDate = dateSource === "MANUAL";
  const [overrideDate, setOverrideDate] = useState("");
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
          cogsPolicy === null
            ? { account: "1405 / 5000", description: "Per the legal entity's COGS policy", debit: `${total} ${currency}`, credit: "" }
            : { account: cogsPolicy ? "5000" : "1405", description: cogsPolicy ? "Cost of goods sold" : "Delivered not invoiced", debit: `${total} ${currency}`, credit: "" },
          { account: "1400", description: "Inventory", debit: "", credit: `${total} ${currency}` },
        ]
      : [];

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (manualDate && !/^\d{4}-\d{2}-\d{2}$/.test(postingDate)) found.postingDate = "Enter the posting date.";
    if (dateSource === "TODAY" && overrideDate !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(overrideDate)) found.postingDate = "Enter a date, or leave it empty for today.";
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
          ...(manualDate ? { postingDate } : dateSource === "TODAY" && overrideDate !== "" ? { postingDate: overrideDate } : {}),
          currency: currency.trim(),
          ...(countryCode.trim() ? { countryCode: countryCode.trim() } : {}),
          asnEnabled,
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
        {manualDate ? (
          <FormField id="post-date" label="Posting date" error={errors.postingDate}>
            {(control) => <Input {...control} type="date" value={postingDate} invalid={Boolean(errors.postingDate)} onChange={(event) => { setPostingDate(event.target.value); key.renew(); }} />}
          </FormField>
        ) : dateSource === "TODAY" ? (
          <FormField
            id="post-date"
            label="Posting date"
            hint={`Empty: today in the warehouse time zone (${operationRule?.timeZone}).`}
            error={errors.postingDate}
            optional
          >
            {(control) => <Input {...control} type="date" value={overrideDate} invalid={Boolean(errors.postingDate)} onChange={(event) => { setOverrideDate(event.target.value); key.renew(); }} />}
          </FormField>
        ) : (
          <p className={styles.note} id="post-date-source">
            {`The posting date is the day of the post in the warehouse time zone (${operationRule?.timeZone}).`}
          </p>
        )}
        <FormField id="post-currency" label="Currency" error={errors.currency}>
          {(control) => <Input {...control} maxLength={3} value={currency} invalid={Boolean(errors.currency)} onChange={(event) => { setCurrency(event.target.value.toUpperCase()); key.renew(); }} />}
        </FormField>
        <FormField id="post-country" label="Ship-to country" hint="Applies that country's packaging fee rates (statistical, not journaled). Empty: no packaging fee." error={errors.countryCode} optional>
          {(control) => <Input {...control} maxLength={2} value={countryCode} invalid={Boolean(errors.countryCode)} onChange={(event) => { setCountryCode(event.target.value.toUpperCase()); key.renew(); }} />}
        </FormField>
        <p className={styles.note} id="post-cogs">
          {cogsPolicy === null
            ? "Cost of goods sold posts per the legal entity's policy, which could not be read."
            : cogsPolicy
              ? "Cost of goods sold posts at shipment (Dr 5000), the legal entity's policy."
              : "The slip posts to delivered not invoiced (Dr 1405), the legal entity's policy."}
        </p>
        <FormField id="post-asn" label="Send an advance ship notice (ASN)" hint="Publishes the containers, SSCCs and quantities of this slip for EDI (FR-28.9). Preset from the legal entity setting.">
          {(control) => <Input {...control} type="checkbox" checked={asnEnabled} onChange={(event) => { asnTouched.current = true; setAsnEnabled(event.target.checked); key.renew(); }} />}
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
