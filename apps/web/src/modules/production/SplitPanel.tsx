import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import {
  describeProblem,
  newIdempotencyKey,
  splitOrder,
  type Problem,
  type ProductionOrder,
  type SplitInput,
  type SplitResult,
} from "./api/client";
import { addDecimals, isZero, negate } from "./labels";
import styles from "./pages.module.css";

const quantityPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;

export interface SplitPanelProps {
  order: ProductionOrder;
  /** Called with both orders after a split; the page shows the original's new version. */
  onSplit(result: SplitResult): void;
  /** Opens the new order; the link is hidden without it. */
  onOpenOrder?(orderId: string): void;
}

interface SplitValues {
  splitQty: string;
  minimumQty: string;
  multiple: string;
}

type SplitErrors = Partial<Record<keyof SplitValues, string>>;

const empty: SplitValues = { splitQty: "", minimumQty: "", multiple: "" };

/** Checks the form; the service applies the same rules (FR-21.15, PROD-0015). */
export function validateSplit(values: SplitValues, orderQty: string): SplitErrors {
  const errors: SplitErrors = {};
  if (!quantityPattern.test(values.splitQty) || isZero(values.splitQty)) {
    errors.splitQty = "Enter a positive quantity with up to 6 decimals.";
  } else {
    const left = addDecimals([orderQty, negate(values.splitQty)]);
    if (isZero(left) || left.startsWith("-")) {
      errors.splitQty = `Enter less than the order quantity (${orderQty}); the original keeps the rest.`;
    }
  }
  for (const name of ["minimumQty", "multiple"] as const) {
    if (values[name] !== "" && !quantityPattern.test(values[name])) {
      errors[name] = "Enter a quantity with up to 6 decimals, or leave it empty.";
    }
  }
  return errors;
}

/** Splits part of a Created to Released order off into a new order (FR-21.15). */
export function SplitPanel({ order, onSplit, onOpenOrder }: SplitPanelProps) {
  const [values, setValues] = useState<SplitValues>(empty);
  const [errors, setErrors] = useState<SplitErrors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<ProductionOrder | null>(null);
  // One Idempotency-Key per version and body, so a retry after a lost response replays.
  const keys = useRef(new Map<string, string>());

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateSplit(values, order.quantity);
    setErrors(found);
    if (Object.keys(found).length > 0 || busy) {
      return;
    }
    const input: SplitInput = { splitQty: values.splitQty };
    if (values.minimumQty !== "") {
      input.minimumQty = values.minimumQty;
    }
    if (values.multiple !== "") {
      input.multiple = values.multiple;
    }
    const slot = `${order.version}:${JSON.stringify(input)}`;
    const key = keys.current.get(slot) ?? newIdempotencyKey();
    keys.current.set(slot, key);
    setBusy(true);
    setFailure(null);
    setCreated(null);
    try {
      const result = await splitOrder(order.id, order.version, input, key);
      setCreated(result.new);
      setValues(empty);
      onSplit(result);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  const field = (name: keyof SplitValues) => ({
    inputMode: "decimal" as const,
    value: values[name],
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [name]: event.target.value }),
  });

  return (
    <Card title="Split order">
      <div className={styles.stack}>
        <p className={styles.hint}>
          The new order receives the split quantity with the same BOM, route and status; this order keeps the rest. Both are
          re-exploded and, once estimated, re-estimated.
        </p>
        <Alert tone="success" live="status">
          {created ? (
            <span className={styles.actions}>
              {`Split off ${created.orderNumber} for ${created.quantity}.`}
              {onOpenOrder ? (
                <Button variant="ghost" size="sm" onClick={() => onOpenOrder(created.id)}>
                  Open {created.orderNumber}
                </Button>
              ) : null}
            </span>
          ) : null}
        </Alert>
        {failure ? (
          <Alert tone="danger" live="alert" title="The split was refused">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <form className={styles.commandForm} onSubmit={submit} noValidate aria-label="Split order">
          <FormField id="split-quantity" label="Split quantity" error={errors.splitQty}>
            {(control) => <Input {...control} {...field("splitQty")} />}
          </FormField>
          <FormField id="split-minimum" label="Minimum order quantity" optional error={errors.minimumQty}>
            {(control) => <Input {...control} {...field("minimumQty")} />}
          </FormField>
          <FormField id="split-multiple" label="Order multiple" optional error={errors.multiple}>
            {(control) => <Input {...control} {...field("multiple")} />}
          </FormField>
          <Button type="submit" icon="box" loading={busy}>
            Split off
          </Button>
        </form>
      </div>
    </Card>
  );
}
