import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  adjustReturnable,
  describeProblem,
  listContainerTypes,
  listReturnableMovements,
  type ContainerType,
  type Problem,
  type ReturnableMovement,
  type ReturnableMovementKind,
} from "./api/client";
import { CONTAINER_CODE, dash, formatDateTime, shortId, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Most movements one view shows, newest first. */
export const MOVEMENT_LIMIT = 200;

const KINDS: Record<ReturnableMovementKind, { label: string; tone: BadgeTone }> = {
  SHIPPED: { label: "Shipped", tone: "info" },
  SHIPMENT_CANCELLED: { label: "Slip cancelled", tone: "warning" },
  RETURNED: { label: "Returned", tone: "success" },
  ADJUSTED: { label: "Corrected", tone: "neutral" },
};

const columns: DataTableColumn<ReturnableMovement>[] = [
  { key: "createdAt", header: "When", render: (row) => formatDateTime(row.createdAt) },
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "containerType", header: "Container type", mono: true },
  {
    key: "kind",
    header: "Movement",
    render: (row) => <Badge tone={KINDS[row.kind]?.tone ?? "neutral"}>{KINDS[row.kind]?.label ?? row.kind}</Badge>,
  },
  { key: "qty", header: "Quantity", align: "end", render: (row) => (row.qty.startsWith("-") ? row.qty : `+${row.qty}`) },
  { key: "balanceAfter", header: "Balance after", align: "end" },
  {
    key: "reference",
    header: "Reference",
    render: (row) =>
      row.kind === "SHIPPED" || row.kind === "SHIPMENT_CANCELLED" ? (
        row.reference ? <Link to={`/fulfillment/packing-slips/${row.reference}`}>Packing slip</Link> : dash
      ) : (
        (row.reference ?? dash)
      ),
  },
  { key: "reason", header: "Reason", render: (row) => row.reason ?? dash },
  { key: "actor", header: "By", mono: true, render: (row) => (row.actor ? shortId(row.actor) : dash) },
];

interface Filter {
  customerId: string;
  containerType: string;
  kind: string;
}

const noFilter: Filter = { customerId: "", containerType: "", kind: "" };

interface Values {
  customerId: string;
  containerType: string;
  delta: string;
  reason: string;
  reference: string;
}

const blank: Values = { customerId: "", containerType: "", delta: "", reason: "", reference: "" };

function validateFilter(f: Filter): Record<string, string> {
  const found: Record<string, string> = {};
  if (f.customerId !== "" && !UUID.test(f.customerId)) found.customerId = "Enter the customer id (a UUID), or leave it empty.";
  if (f.containerType !== "" && !CONTAINER_CODE.test(f.containerType)) found.containerType = "Enter a container type code, or leave it empty.";
  return found;
}

function validateAdjustment(v: Values): Record<string, string> {
  const found: Record<string, string> = {};
  if (!UUID.test(v.customerId.trim())) found.customerId = "Enter the customer id (a UUID).";
  if (v.containerType === "") found.containerType = "Choose the returnable container type.";
  if (!/^-?[1-9]\d{0,8}$/.test(v.delta.trim())) found.delta = "Enter a whole number of containers, not zero; negative takes containers off the balance.";
  const reason = v.reason.trim();
  if (reason.length < 3 || reason.length > 200) found.reason = "Give the reason, 3 to 200 characters.";
  if (v.reference.trim().length > 100) found.reference = "Keep the reference to 100 characters.";
  return found;
}

/**
 * The history behind the returnable packaging balances (FR-28.16): containers that
 * shipped, came back with a cancelled slip or a customer, and manual corrections.
 * Someone holding fulfillment.returnable-packaging.maintain can correct a balance here, with a reason;
 * the service enforces the duty.
 */
export function ReturnableMovementsPage() {
  const duties = usePrincipalDuties();
  const canAdjust = holdsDuty(duties, "fulfillment.returnable-packaging.maintain");
  const [fields, setFields] = useState<Filter>(noFilter);
  const [filter, setFilter] = useState<Filter>(noFilter);
  const [filterErrors, setFilterErrors] = useState<Record<string, string>>({});
  const state = useLoad(
    async (signal) => {
      const [movements, types] = await Promise.all([
        listReturnableMovements(
          { customerId: filter.customerId || undefined, containerType: filter.containerType || undefined, kind: filter.kind || undefined, limit: MOVEMENT_LIMIT },
          signal,
        ),
        canAdjust ? listContainerTypes(signal) : Promise.resolve<ContainerType[]>([]),
      ]);
      return { movements, types: types.filter((t) => t.returnable) };
    },
    `${filter.customerId}/${filter.containerType}/${filter.kind}/${canAdjust}`,
  );
  const [values, setValues] = useState<Values>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function set(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.renew();
  }

  function apply(event: FormEvent) {
    event.preventDefault();
    const trimmed = { customerId: fields.customerId.trim(), containerType: fields.containerType.trim(), kind: fields.kind };
    const found = validateFilter(trimmed);
    setFilterErrors(found);
    if (Object.keys(found).length === 0) setFilter(trimmed);
  }

  function clear() {
    setFields(noFilter);
    setFilter(noFilter);
    setFilterErrors({});
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAdjustment(values);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await adjustReturnable(
        {
          customerId: values.customerId.trim(),
          containerType: values.containerType,
          delta: values.delta.trim(),
          reason: values.reason.trim(),
          reference: values.reference.trim() || undefined,
        },
        key.current(),
      );
      key.renew();
      setValues(blank);
      setNotice(`Corrected. Customer ${shortId(saved.customerId)} now holds ${saved.balanceAfter} ${saved.containerType}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  const filtered = filter.customerId !== "" || filter.containerType !== "" || filter.kind !== "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Returnable packaging movements"
        description="Every change of a customer's returnable pallets and crates, newest first: shipped on a slip, back when a slip is cancelled, returned by the customer, or corrected by hand."
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/returnable-packaging">
            Balances and return receipts
          </Link>
        }
      />
      <Card title="Filter">
        <form className={styles.form} onSubmit={apply} noValidate>
          <FormField id="mv-customer" label="Customer" error={filterErrors.customerId} optional>
            {(control) => <EntityLookup entity="customer" {...control} value={fields.customerId} invalid={Boolean(filterErrors.customerId)} onChange={(event) => setFields({ ...fields, customerId: event.target.value })} />}
          </FormField>
          <FormField id="mv-type" label="Container type" error={filterErrors.containerType} optional>
            {(control) => <Input {...control} value={fields.containerType} invalid={Boolean(filterErrors.containerType)} onChange={(event) => setFields({ ...fields, containerType: event.target.value })} />}
          </FormField>
          <FormField id="mv-kind" label="Movement" optional>
            {(control) => (
              <Select {...control} value={fields.kind} onChange={(event) => setFields({ ...fields, kind: event.target.value })}>
                <option value="">Every movement</option>
                {Object.entries(KINDS).map(([kind, { label }]) => (
                  <option key={kind} value={kind}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filter
            </Button>
            {filtered ? (
              <Button type="button" onClick={clear}>
                Clear filter
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load the movements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Returnable packaging movements"
          columns={columns}
          rows={state.data?.movements ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            filtered
              ? { title: "No movements match this filter", description: "Clear the filter to see every movement." }
              : { title: "No returnable packaging movements yet", description: "Movements appear when a packing slip ships returnable containers, a customer returns them or a balance is corrected." }
          }
        />
      )}
      {state.data && state.data.movements.length >= MOVEMENT_LIMIT ? <p className={styles.note}>Showing the newest {MOVEMENT_LIMIT} movements. Filter to see older ones.</p> : null}
      {canAdjust ? (
        <Card title="Correct a balance">
          <form className={styles.form} onSubmit={submit} noValidate>
            <p className={styles.note}>
              For an opening balance, a count at the customer or packaging lost in transit. The reason is kept with the movement. A correction that would take the balance below zero is refused (FUL-0015).
            </p>
            <FormField id="adj-customer" label="Customer" error={errors.customerId}>
              {(control) => <EntityLookup entity="customer" {...control} value={values.customerId} invalid={Boolean(errors.customerId)} onChange={(event) => set({ customerId: event.target.value })} />}
            </FormField>
            <FormField id="adj-type" label="Container type" error={errors.containerType}>
              {(control) => (
                <Select {...control} value={values.containerType} onChange={(event) => set({ containerType: event.target.value })}>
                  <option value="">Choose a returnable type</option>
                  {(state.data?.types ?? []).map((t) => (
                    <option key={t.code} value={t.code}>
                      {t.code}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="adj-delta" label="Change in containers" hint="Negative takes containers off the balance." error={errors.delta}>
              {(control) => <Input {...control} inputMode="numeric" value={values.delta} invalid={Boolean(errors.delta)} onChange={(event) => set({ delta: event.target.value })} />}
            </FormField>
            <FormField id="adj-reason" label="Reason" error={errors.reason}>
              {(control) => <Input {...control} value={values.reason} invalid={Boolean(errors.reason)} onChange={(event) => set({ reason: event.target.value })} />}
            </FormField>
            <FormField id="adj-reference" label="Reference" hint="A count sheet or delivery note." error={errors.reference} optional>
              {(control) => <Input {...control} value={values.reference} invalid={Boolean(errors.reference)} onChange={(event) => set({ reference: event.target.value })} />}
            </FormField>
            <ProblemAlert title="Could not correct the balance" problem={failure} />
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Correct balance
              </Button>
              {notice ? (
                <p role="status" className={styles.note}>
                  {notice}
                </p>
              ) : null}
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
