import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createServiceObject,
  describeProblem,
  listServiceObjects,
  newKey,
  OBJECT_STATUSES,
  receiveShipment,
  WARRANTY_TRIGGERS,
  type ObjectStatus,
  type Problem,
  type ServiceObject,
  type ServiceObjectInput,
  type ShipmentInput,
  type ShipmentReceipt,
  type WarrantyTerm,
  type WarrantyTrigger,
} from "./api/client";
import { dash, DATE, sentence, shortId, today, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

/** "Parts, labor" or "No warranty". */
export function warrantySummary(w: WarrantyTerm): string {
  if (w.months === 0) return "No warranty";
  const covers = [w.partsCovered ? "parts" : "", w.laborCovered ? "labor" : "", w.travelCovered ? "travel" : ""].filter(Boolean);
  return `${w.months} months from ${sentence(w.startTrigger).toLowerCase()}${covers.length > 0 ? `, ${covers.join(", ")}` : ", nothing covered"}`;
}

/** Whether the warranty covers today; null while it has not started. */
export function warrantyActive(o: Pick<ServiceObject, "warrantyStartsOn" | "warrantyEndsOn">, on: string = today()): boolean | null {
  if (!o.warrantyStartsOn) return null;
  return o.warrantyStartsOn <= on && (o.warrantyEndsOn === undefined || on < o.warrantyEndsOn);
}

/** The warranty of a unit today: none, not started, active until its end, or expired. */
export function WarrantyState({ object }: { object: ServiceObject }) {
  const active = warrantyActive(object);
  if (object.warranty.months === 0) return <Badge tone="neutral">No warranty</Badge>;
  if (active === null) return <Badge tone="neutral">Not started</Badge>;
  return active ? <Badge tone="success">{`Until ${object.warrantyEndsOn ?? dash}`}</Badge> : <Badge tone="warning">Expired</Badge>;
}

const columns: DataTableColumn<ServiceObject>[] = [
  { key: "serial", header: "Serial", mono: true },
  { key: "itemId", header: "Item", mono: true, render: (row) => shortId(row.itemId) },
  { key: "customerPartyId", header: "Customer", mono: true, render: (row) => shortId(row.customerPartyId) },
  { key: "installedOn", header: "Installed", mono: true, render: (row) => row.installedOn ?? dash },
  { key: "warranty", header: "Warranty", render: (row) => <WarrantyState object={row} /> },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** Installed base explorer (PRD 8.45.6): search by customer or serial. */
export function ServiceObjectsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ObjectStatus | "">("");
  const [customer, setCustomer] = useState("");
  const [serial, setSerial] = useState("");
  const customerFilter = UUID.test(customer.trim()) ? customer.trim() : "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Installed base"
        description="Every serialized unit at a customer site, with its warranty. A case on a unit bills its covered lines to warranty."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/service/objects/receive">
              Receive shipment
            </Link>
            <Link className={buttonClassName("primary")} to="/service/objects/new">
              Register unit
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="objects-customer" label="Customer" hint={customer && !customerFilter ? "Enter a full UUID to filter." : undefined} optional>
          {(control) => <Input {...control} className={styles.mono} value={customer} onChange={(e) => setCustomer(e.target.value)} />}
        </FormField>
        <FormField id="objects-serial" label="Serial" optional>
          {(control) => <Input {...control} className={styles.mono} value={serial} onChange={(e) => setSerial(e.target.value)} />}
        </FormField>
        <FormField id="objects-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as ObjectStatus | "")}>
              <option value="">Any status</option>
              {OBJECT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <PagedTable
        label="Service objects"
        columns={columns}
        load={(cursor, signal) => listServiceObjects({ customerPartyId: customerFilter, serial: serial.trim(), status, cursor }, signal)}
        loadKey={`${customerFilter}|${serial.trim()}|${status}`}
        empty={{ title: "No service objects found", description: "Register a unit or receive a shipment of serialized items." }}
        onRowOpen={(row) => navigate(`/service/objects/${row.id}`)}
      />
    </div>
  );
}

export interface WarrantyValues {
  startTrigger: WarrantyTrigger;
  months: string;
  partsCovered: boolean;
  laborCovered: boolean;
  travelCovered: boolean;
}

export const defaultWarranty = (): WarrantyValues => ({ startTrigger: "SHIP", months: "12", partsCovered: true, laborCovered: false, travelCovered: false });

function warrantyOf(values: WarrantyValues, errors: Errors, key: string): WarrantyTerm {
  const months = Number(values.months);
  if (!Number.isInteger(months) || months < 0 || months > 240) errors[key] = "Warranty months must be a whole number from 0 to 240.";
  return { startTrigger: values.startTrigger, months, partsCovered: values.partsCovered, laborCovered: values.laborCovered, travelCovered: values.travelCovered };
}

export interface ObjectValues {
  itemId: string;
  serial: string;
  customerPartyId: string;
  legalEntityId: string;
  parentId: string;
  shippedOn: string;
  installedOn: string;
  warranty: WarrantyValues;
}

/** Checks that mirror the service (objects.go createServiceObject). */
export function validateObject(values: ObjectValues): { errors: Errors; input?: ServiceObjectInput } {
  const errors: Errors = {};
  if (!UUID.test(values.itemId.trim())) errors.itemId = "Enter the item's UUID.";
  if (values.serial === "" || values.serial.trim() !== values.serial || [...values.serial].length > 100) errors.serial = "Enter 1 to 100 characters without leading or trailing spaces.";
  if (!UUID.test(values.customerPartyId.trim())) errors.customerPartyId = "Enter the customer's UUID.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  const parent = values.parentId.trim();
  if (parent && !UUID.test(parent)) errors.parentId = "Enter the parent unit's UUID, or leave it empty.";
  if (values.shippedOn && !DATE.test(values.shippedOn)) errors.shippedOn = "Choose a date or leave it empty.";
  if (values.installedOn && !DATE.test(values.installedOn)) errors.installedOn = "Choose a date or leave it empty.";
  const warranty = warrantyOf(values.warranty, errors, "warranty");
  if (values.warranty.startTrigger === "SHIP" && !values.shippedOn && warranty.months > 0 && !errors.warranty) {
    errors.shippedOn = "A warranty from shipment needs the ship date.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      itemId: values.itemId.trim(),
      serial: values.serial,
      customerPartyId: values.customerPartyId.trim(),
      legalEntityId: values.legalEntityId.trim(),
      ...(parent ? { parentId: parent } : {}),
      ...(values.shippedOn ? { shippedOn: values.shippedOn } : {}),
      ...(values.installedOn ? { installedOn: values.installedOn } : {}),
      warranty,
    },
  };
}

function WarrantyFields({ idPrefix, values, error, onChange }: { idPrefix: string; values: WarrantyValues; error?: string; onChange(patch: Partial<WarrantyValues>): void }) {
  return (
    <fieldset className={`${styles.wide} ${styles.lineCard}`}>
      <legend className={styles.lineLegend}>Warranty</legend>
      <FormField id={`${idPrefix}-trigger`} label="Starts on" hint="Shipment, installation, or the day the unit is registered here.">
        {(control) => (
          <Select {...control} value={values.startTrigger} onChange={(e) => onChange({ startTrigger: e.target.value as WarrantyTrigger })}>
            {WARRANTY_TRIGGERS.map((t) => (
              <option key={t} value={t}>
                {t === "SHIP" ? "Ship date" : t === "INSTALL" ? "Installation date" : "Registration date"}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id={`${idPrefix}-months`} label="Months" hint="0 means no warranty." error={error}>
        {(control) => <Input {...control} inputMode="numeric" value={values.months} onChange={(e) => onChange({ months: e.target.value })} invalid={Boolean(error)} />}
      </FormField>
      <fieldset className={styles.groups}>
        <legend className={styles.groupsLegend}>Covered</legend>
        {(["partsCovered", "laborCovered", "travelCovered"] as const).map((flag) => (
          <span key={flag} className={styles.check}>
            <Input id={`${idPrefix}-${flag}`} type="checkbox" checked={values[flag]} onChange={(e) => onChange({ [flag]: e.target.checked })} />
            <label htmlFor={`${idPrefix}-${flag}`}>{flag === "partsCovered" ? "Parts" : flag === "laborCovered" ? "Labor" : "Travel"}</label>
          </span>
        ))}
      </fieldset>
    </fieldset>
  );
}

function FormFailure({ failure, title }: { failure: Problem | null; title: string }) {
  if (!failure) return null;
  return (
    <div className={styles.formActions}>
      <Alert tone="danger" title={title} live="alert">
        <ProblemMessage problem={failure} />
      </Alert>
    </div>
  );
}

/** Register one installed unit by hand (FR-45.1), optionally under a parent unit. */
export function NewServiceObjectPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<ObjectValues>({
    itemId: "",
    serial: "",
    customerPartyId: params.get("customerPartyId") ?? "",
    legalEntityId: params.get("legalEntityId") ?? "",
    parentId: params.get("parentId") ?? "",
    shippedOn: "",
    installedOn: "",
    warranty: defaultWarranty(),
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ObjectValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateObject(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createServiceObject(input, key.current);
      navigate(`/service/objects/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof ObjectValues, "warranty">, label: string, hint?: string, extra: { type?: string; optional?: boolean } = {}): ReactNode => (
    <FormField id={`object-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.type ? undefined : styles.mono}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Register service object" description="One serial of an item at a customer. The serial is unique per item." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Register service object">
          {text("itemId", "Item", "UUID of the serialized item.")}
          {text("serial", "Serial number")}
          {text("customerPartyId", "Customer", "UUID of the customer party.")}
          {text("legalEntityId", "Legal entity", "UUID of the servicing legal entity.")}
          {text("parentId", "Part of", "UUID of the unit this one is installed in.", { optional: true })}
          {text("shippedOn", "Shipped on", undefined, { type: "date", optional: true })}
          {text("installedOn", "Installed on", undefined, { type: "date", optional: true })}
          <WarrantyFields idPrefix="object-warranty" values={values.warranty} error={errors.warranty} onChange={(patch) => update({ warranty: { ...values.warranty, ...patch } })} />
          <FormFailure failure={failure} title={failure?.code === "SVC-OBJ-001" ? "This serial is already registered" : "Could not register the unit"} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Register unit
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/objects">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

export interface ShipmentValues {
  shipmentId: string;
  shipDate: string;
  customerPartyId: string;
  legalEntityId: string;
  itemId: string;
  serials: string;
  warranty: WarrantyValues;
}

/** Checks that mirror the service (objects.go receiveShipment). Serials are one per line. */
export function validateShipment(values: ShipmentValues): { errors: Errors; input?: ShipmentInput } {
  const errors: Errors = {};
  if (!UUID.test(values.shipmentId.trim())) errors.shipmentId = "Enter the shipment's UUID.";
  if (!DATE.test(values.shipDate)) errors.shipDate = "Choose the ship date.";
  if (!UUID.test(values.customerPartyId.trim())) errors.customerPartyId = "Enter the customer's UUID.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.itemId.trim())) errors.itemId = "Enter the item's UUID.";
  const serials = values.serials
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
  if (serials.length < 1 || serials.length > 500) errors.serials = "Enter 1 to 500 serial numbers, one per line.";
  else if (new Set(serials).size !== serials.length) errors.serials = "Each serial number may appear once.";
  else if (serials.some((s) => [...s].length > 100)) errors.serials = "A serial number has at most 100 characters.";
  const warranty = warrantyOf(values.warranty, errors, "warranty");
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      shipmentId: values.shipmentId.trim(),
      shipDate: values.shipDate,
      customerPartyId: values.customerPartyId.trim(),
      legalEntityId: values.legalEntityId.trim(),
      lines: [{ itemId: values.itemId.trim(), serials, warranty }],
    },
  };
}

/** Create the installed base of a confirmed serialized shipment (FR-45.1, AC-45.8). */
export function ReceiveShipmentPage() {
  const [values, setValues] = useState<ShipmentValues>({
    shipmentId: "",
    shipDate: today(),
    customerPartyId: "",
    legalEntityId: "",
    itemId: "",
    serials: "",
    warranty: defaultWarranty(),
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [receipt, setReceipt] = useState<ShipmentReceipt | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<ShipmentValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateShipment(values);
    setErrors(found);
    setFailure(null);
    setReceipt(null);
    if (!input) return;
    setBusy(true);
    try {
      setReceipt(await receiveShipment(input));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof ShipmentValues, "warranty" | "serials">, label: string, hint?: string, type?: string) => (
    <FormField id={`shipment-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input {...control} type={type ?? "text"} className={type ? undefined : styles.mono} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Receive shipment"
        description="Each shipped serial becomes an active unit at the customer. Receiving the same shipment again creates nothing twice."
      />
      <Alert tone="success" live="status">
        {receipt ? `${receipt.created} ${receipt.created === 1 ? "unit" : "units"} created, ${receipt.existing} already registered.` : ""}
      </Alert>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Receive shipment">
          {text("shipmentId", "Shipment", "UUID of the confirmed shipment.")}
          {text("shipDate", "Ship date", undefined, "date")}
          {text("customerPartyId", "Customer", "UUID of the customer party.")}
          {text("legalEntityId", "Legal entity", "UUID of the servicing legal entity.")}
          {text("itemId", "Item", "UUID of the serialized item.")}
          <FormField id="shipment-serials" label="Serial numbers" hint="One per line, up to 500." error={errors.serials ?? errors["lines[0].serials"]}>
            {(control) => <Textarea {...control} className={styles.mono} value={values.serials} onChange={(e) => update({ serials: e.target.value })} invalid={Boolean(errors.serials)} />}
          </FormField>
          <WarrantyFields idPrefix="shipment-warranty" values={values.warranty} error={errors.warranty} onChange={(patch) => update({ warranty: { ...values.warranty, ...patch } })} />
          <FormFailure failure={failure} title="Could not receive the shipment" />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create units
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/objects">
              Back to installed base
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
