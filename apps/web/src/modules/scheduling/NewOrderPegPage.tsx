import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, EmptyState, FormField, PageHeader, Select, Skeleton } from "../../shell/ui";
import { createPeg, describeProblem, listOperations, newKey, type NewOrderPeg, type Problem } from "./api/client";
import { shortId, UUID } from "./format";
import { mapFields } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface PegValues {
  parentOrderId: string;
  dependentOrderId: string;
}

type Errors = Partial<Record<keyof PegValues, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validatePeg(values: PegValues): { errors: Errors; input?: NewOrderPeg } {
  const errors: Errors = {};
  if (!UUID.test(values.parentOrderId)) errors.parentOrderId = "Choose the order whose output is used.";
  if (!UUID.test(values.dependentOrderId)) errors.dependentOrderId = "Choose the order that uses it.";
  else if (values.dependentOrderId === values.parentOrderId) errors.dependentOrderId = "An order cannot be pegged to itself.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { parentOrderId: values.parentOrderId, dependentOrderId: values.dependentOrderId } };
}

/** Distinct order ids of the operations in scheduling, with their operation counts. */
export function ordersOf(operations: { orderId: string }[]): { orderId: string; operations: number }[] {
  const counts = new Map<string, number>();
  for (const op of operations) counts.set(op.orderId, (counts.get(op.orderId) ?? 0) + 1);
  return [...counts].map(([orderId, operations]) => ({ orderId, operations })).sort((a, b) => a.orderId.localeCompare(b.orderId));
}

const fieldNames: Record<string, keyof PegValues> = { parentOrderId: "parentOrderId", dependentOrderId: "dependentOrderId" };

/** Peg one order to another whose output it consumes (FR-23.8). */
export function NewOrderPegPage() {
  const navigate = useNavigate();
  const orders = useLoad(async (signal) => ordersOf(await listOperations({ limit: 1000 }, signal)), "peg-orders");
  const [values, setValues] = useState<PegValues>({ parentOrderId: "", dependentOrderId: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PegValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePeg(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createPeg(input, key.current);
      navigate("/scheduling/order-pegs");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = mapFields(problem.fields, fieldNames);
      if (problem.code === "SCHED-0121") fieldErrors.dependentOrderId = "The parent already depends on this order, so the peg would form a loop.";
      if (problem.code === "SCHED-0120") fieldErrors.parentOrderId = "Both orders need operations in scheduling.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const orderSelect = (name: keyof PegValues, placeholder: string) =>
    function Control(control: { id: string }) {
      return (
        <Select {...control} value={values[name]} onChange={(event) => update({ [name]: event.target.value })} invalid={Boolean(errors[name])}>
          <option value="">{placeholder}</option>
          {(orders.data ?? []).map((o) => (
            <option key={o.orderId} value={o.orderId}>
              {`${shortId(o.orderId)} · ${o.operations} operation(s)`}
            </option>
          ))}
        </Select>
      );
    };

  let body;
  if (orders.error) {
    body = (
      <Alert tone="danger" title="Could not load orders" live="alert" action={<Button size="sm" onClick={orders.reload}>Try again</Button>}>
        {orders.error.message} ({orders.error.code})
      </Alert>
    );
  } else if (!orders.data) {
    body = (
      <div className={styles.loading} aria-busy="true">
        <span className="visually-hidden">Loading orders…</span>
        <Skeleton height="control" />
        <Skeleton height="control" />
      </div>
    );
  } else if (orders.data.length < 2) {
    body = (
      <EmptyState
        headingLevel={3}
        title="Add operations for two orders first"
        description="A peg links two orders that both have operations in scheduling."
        action={
          <Link className={buttonClassName("secondary")} to="/scheduling/operations/new">
            Add an operation
          </Link>
        }
      />
    );
  } else {
    body = (
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="peg-parent" label="Parent order" hint="The order whose output is used." error={errors.parentOrderId}>
          {orderSelect("parentOrderId", "Choose the parent order")}
        </FormField>
        <FormField id="peg-dependent" label="Dependent order" hint="The order that uses it; it waits for the parent." error={errors.dependentOrderId}>
          {orderSelect("dependentOrderId", "Choose the dependent order")}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not create the peg" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Create peg
          </Button>
          <Link className={buttonClassName("ghost")} to="/scheduling/order-pegs">
            Cancel
          </Link>
        </div>
      </form>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New order peg" description="Enter pegs here until master planning publishes them." />
      <Card>{body}</Card>
    </div>
  );
}
