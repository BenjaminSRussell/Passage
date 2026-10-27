import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, PageHeader } from "../../shell/ui";
import { previewCentralPayment, previewReversal, type CentralPaymentRequest, type PaymentDirection } from "./api/client";
import { Fields, type FieldSpec } from "./Fields";
import { isCurrency, isPositive } from "./format";
import { checkMatrix, emptyMatrixRow, legColumns, matrixFields, type MatrixKey } from "./matrix";
import { RowGrid } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type Key = "payingEntity" | "owningEntity" | "paymentMethod" | "direction" | "amount" | "currency" | "bankAccount" | "owningAccount";

export interface PaymentValues {
  payment: Record<Key, string>;
  matrix: Record<MatrixKey, string>[];
}

const fields: FieldSpec<Key>[] = [
  {
    name: "direction",
    label: "Direction",
    options: [
      { value: "Pay", label: "Pay a vendor invoice on behalf" },
      { value: "Receive", label: "Receive a customer payment on behalf" },
    ],
  },
  { name: "payingEntity", label: "Paying entity", mono: true, hint: "The entity whose bank account pays or receives." },
  { name: "owningEntity", label: "Owning entity", mono: true, hint: "The entity that owns the open invoice." },
  { name: "paymentMethod", label: "Payment method", mono: true },
  { name: "amount", label: "Amount", decimal: true },
  { name: "currency", label: "Currency", mono: true },
  { name: "bankAccount", label: "Bank account (paying entity)", mono: true },
  { name: "owningAccount", label: "Vendor or customer account (owning entity)", mono: true },
];

/** AC-40.10: P pays S's vendor invoice of EUR 10,000.00. */
export const ac4010: PaymentValues = {
  payment: {
    direction: "Pay",
    payingEntity: "P",
    owningEntity: "S",
    paymentMethod: "SEPA",
    amount: "10000.00",
    currency: "EUR",
    bankAccount: "1100-BANK",
    owningAccount: "2000-VENDOR",
  },
  matrix: [
    { entityA: "P", entityB: "S", dueFrom: "1510-DUE-FROM-S", dueTo: "2510-DUE-TO-P" },
    { entityA: "S", entityB: "P", dueFrom: "1520-DUE-FROM-P", dueTo: "2520-DUE-TO-S" },
  ],
};

/** Checks that mirror the service, and the request the form sends. */
export function validatePayment(values: PaymentValues): Checked<CentralPaymentRequest> {
  const errors: Record<string, string> = {};
  const v = (k: Key) => values.payment[k].trim();
  const currency = v("currency").toUpperCase();
  if (v("payingEntity") === "") errors.payingEntity = "Enter the paying entity.";
  if (v("owningEntity") === "") errors.owningEntity = "Enter the owning entity.";
  else if (v("owningEntity") === v("payingEntity")) errors.owningEntity = "The owning entity must differ from the paying entity.";
  if (v("paymentMethod") === "") errors.paymentMethod = "Enter the payment method.";
  if (!isPositive(v("amount"))) errors.amount = "Enter an amount above 0 with at most 6 decimals.";
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as EUR.";
  if (v("bankAccount") === "") errors.bankAccount = "Enter the bank account.";
  if (v("owningAccount") === "") errors.owningAccount = "Enter the vendor or customer account.";
  const matrix = checkMatrix(values.matrix, errors);
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      payingEntity: v("payingEntity"),
      owningEntity: v("owningEntity"),
      paymentMethod: v("paymentMethod"),
      direction: v("direction") as PaymentDirection,
      amount: v("amount"),
      currency,
      bankAccount: v("bankAccount"),
      owningAccount: v("owningAccount"),
      matrix,
    },
  };
}

/** Derive the postings of one centralized payment in the paying and owning entities (FR-40.12, AC-40.10). */
export function CentralPaymentsPage() {
  const [values, setValues] = useState<PaymentValues>(ac4010);
  const [sent, setSent] = useState<CentralPaymentRequest | null>(null);
  const preview = usePreview(async (req: CentralPaymentRequest) => {
    const res = await previewCentralPayment(req);
    setSent(req);
    return res;
  });
  const reversal = usePreview(previewReversal);
  const { errors, failure, busy, result } = preview;

  function submit(event: FormEvent) {
    event.preventDefault();
    setSent(null);
    void preview.run(validatePayment(values));
  }

  function reverse() {
    if (!sent || !result) return;
    void reversal.run({ errors: {}, request: { ...sent, pairId: result.pairId } });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Centralized payments"
        description="A paying entity settles another entity's vendor invoice, or receives its customer's payment, and both entities post due-to and due-from under one pair id. A payment the bank returns reverses in both entities. Storing the setup, payment proposals across entities and settling the open item arrive with the payments slice."
      />
      <Card title="Payment on behalf" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="cp" fields={fields} values={values.payment} errors={errors} onChange={(payment) => setValues({ ...values, payment })} />
          <h4 className={styles.section}>Accounting matrix</h4>
          <RowGrid
            idPrefix="cp-matrix"
            path="matrix"
            noun="Matrix row"
            fields={matrixFields}
            rows={values.matrix}
            errors={errors}
            newRow={emptyMatrixRow}
            onChange={(matrix) => setValues({ ...values, matrix })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The payment postings were not derived" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview postings
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Payment legs" columns={legColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : result ? (
        <>
          <Alert tone={result.balanced ? "success" : "danger"} title={result.balanced ? "Balanced in each entity" : "Not balanced"}>
            Both entities post under pair <span className={styles.mono}>{result.pairId}</span>.
          </Alert>
          <DataTable
            label="Payment legs"
            columns={legColumns}
            rows={result.legs}
            getRowKey={(l, i) => `${l.entity}-${l.account}-${i}`}
            empty={{ title: "No legs", description: "The service returned no payment legs." }}
          />
          <Card title="Bank return" headingLevel={3}>
            <div className={styles.cardBody}>
              <p className={styles.note}>
                When the bank returns the payment, both entities reverse their postings in one saga under the same pair id (BR-40.11).
              </p>
              {reversal.failure ? (
                <Alert tone="danger" title="The reversal was not derived" live="alert">
                  {reversal.failure.message} ({reversal.failure.code})
                </Alert>
              ) : null}
              <div>
                <Button onClick={reverse} loading={reversal.busy} disabled={!sent}>
                  Preview reversal
                </Button>
              </div>
            </div>
          </Card>
          {reversal.busy ? (
            <DataTable label="Reversal legs" columns={legColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
          ) : reversal.result && reversal.result.pairId === result.pairId ? (
            <DataTable
              label="Reversal legs"
              columns={legColumns}
              rows={reversal.result.legs}
              getRowKey={(l, i) => `${l.entity}-${l.account}-${i}`}
              empty={{ title: "No legs", description: "The service returned no reversal legs." }}
            />
          ) : null}
        </>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            headingLevel={3}
            title="No payment postings yet"
            description="The form holds acceptance example AC-40.10. Preview it, or enter your own payment and matrix rows."
          />
        </Card>
      )}
    </div>
  );
}
