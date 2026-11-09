import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, PageHeader, StatGroup, StatTile } from "../../shell/ui";
import { previewCharge, type ChargeCategory, type ChargePreviewRequest, type TransferMethod, type TransferPriceRow } from "./api/client";
import { Fields, type FieldSpec } from "./Fields";
import { amount, isCurrency, isDate, isDecimal, isPositive } from "./format";
import { checkMatrix, emptyMatrixRow, legColumns, matrixFields, type MatrixKey } from "./matrix";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type LineKey =
  | "lendingEntity"
  | "borrowingEntity"
  | "category"
  | "resourceRole"
  | "transactionDate"
  | "quantity"
  | "unitCost"
  | "currency"
  | "costAccount"
  | "recoveryAccount"
  | "marginAccount";
type PriceKey = "category" | "resourceRole" | "method" | "markupPct" | "unitPrice" | "effectiveFrom";

export interface ChargeValues {
  line: Record<LineKey, string>;
  prices: Record<PriceKey, string>[];
  matrix: Record<MatrixKey, string>[];
}

const categories = [
  { value: "Hour", label: "Hour" },
  { value: "Expense", label: "Expense" },
  { value: "Item", label: "Item" },
  { value: "Fee", label: "Fee" },
];

const lineFields: FieldSpec<LineKey>[] = [
  { name: "lendingEntity", label: "Lending entity", mono: true, hint: "The entity whose resource or cost is charged." },
  { name: "borrowingEntity", label: "Borrowing entity", mono: true, hint: "The entity that bears the cost." },
  { name: "category", label: "Category", options: categories },
  { name: "resourceRole", label: "Resource role", optional: true, hint: "Transfer prices are kept per role, such as Consultant." },
  { name: "transactionDate", label: "Transaction date", type: "date", hint: "Selects the transfer price effective on that day." },
  { name: "quantity", label: "Quantity", decimal: true },
  { name: "unitCost", label: "Unit cost", decimal: true, hint: "The lending entity's cost per unit." },
  { name: "currency", label: "Currency", mono: true },
  { name: "costAccount", label: "Borrower cost account", mono: true },
  { name: "recoveryAccount", label: "Lender cost recovery account", mono: true },
  { name: "marginAccount", label: "Lender IC margin account", mono: true },
];

const priceFields: RowField<PriceKey>[] = [
  { name: "category", label: "Category", options: categories },
  { name: "resourceRole", label: "Resource role", optional: true },
  {
    name: "method",
    label: "Method",
    options: [
      { value: "CostPlus", label: "Cost plus %" },
      { value: "Cost", label: "Cost" },
      { value: "Table", label: "Table price" },
    ],
  },
  { name: "markupPct", label: "Markup (%)", decimal: true, optional: true },
  { name: "unitPrice", label: "Unit price", decimal: true, optional: true },
  { name: "effectiveFrom", label: "Effective from", type: "date" },
];

const emptyPrice = (): Record<PriceKey, string> => ({
  category: "Hour",
  resourceRole: "",
  method: "CostPlus",
  markupPct: "",
  unitPrice: "",
  effectiveFrom: "",
});

/** AC-40.11: 8 hours of a lending-entity consultant at cost 60.00 with cost-plus 10%. */
export const ac4011: ChargeValues = {
  line: {
    lendingEntity: "P",
    borrowingEntity: "S",
    category: "Hour",
    resourceRole: "Consultant",
    transactionDate: "2026-11-14",
    quantity: "8",
    unitCost: "60.00",
    currency: "EUR",
    costAccount: "5100-PROJECT-COST",
    recoveryAccount: "4800-COST-RECOVERY",
    marginAccount: "4810-IC-MARGIN",
  },
  prices: [{ category: "Hour", resourceRole: "Consultant", method: "CostPlus", markupPct: "10", unitPrice: "", effectiveFrom: "2026-01-01" }],
  matrix: [{ entityA: "P", entityB: "S", dueFrom: "1510-DUE-FROM-S", dueTo: "2510-DUE-TO-P" }],
};

/** Checks that mirror the service, and the request the form sends. */
export function validateCharge(values: ChargeValues): Checked<ChargePreviewRequest> {
  const errors: Record<string, string> = {};
  const v = (k: LineKey) => values.line[k].trim();
  const currency = v("currency").toUpperCase();
  if (v("lendingEntity") === "") errors.lendingEntity = "Enter the lending entity.";
  if (v("borrowingEntity") === "") errors.borrowingEntity = "Enter the borrowing entity.";
  else if (v("borrowingEntity") === v("lendingEntity")) errors.borrowingEntity = "The borrowing entity must differ from the lending entity.";
  if (!isDate(v("transactionDate"))) errors.transactionDate = "Enter the transaction date.";
  if (!isPositive(v("quantity"))) errors.quantity = "Enter a quantity above 0.";
  if (!isDecimal(v("unitCost"))) errors.unitCost = "Enter the unit cost, 0 or more.";
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as EUR.";
  for (const k of ["costAccount", "recoveryAccount", "marginAccount"] as const) {
    if (v(k) === "") errors[k] = "Enter the account.";
  }
  const transferPrices: TransferPriceRow[] = values.prices.map((row, i) => {
    const at = (name: PriceKey) => `transferPrices[${i}].${name}`;
    const method = row.method as TransferMethod;
    if (!isDate(row.effectiveFrom)) errors[at("effectiveFrom")] = "Enter the date the price applies from.";
    if (method === "CostPlus" && !isDecimal(row.markupPct)) errors[at("markupPct")] = "Enter the markup, 0 or more.";
    if (method === "Table" && !isDecimal(row.unitPrice)) errors[at("unitPrice")] = "Enter the table price, 0 or more.";
    return {
      category: row.category as ChargeCategory,
      resourceRole: row.resourceRole.trim() || undefined,
      method,
      markupPct: method === "CostPlus" ? row.markupPct.trim() : undefined,
      unitPrice: method === "Table" ? row.unitPrice.trim() : undefined,
      effectiveFrom: row.effectiveFrom.trim(),
    };
  });
  if (transferPrices.length === 0) errors.transferPrices = "Add at least one transfer price.";
  const matrix = checkMatrix(values.matrix, errors);
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      lendingEntity: v("lendingEntity"),
      borrowingEntity: v("borrowingEntity"),
      category: v("category") as ChargeCategory,
      resourceRole: v("resourceRole") || undefined,
      transactionDate: v("transactionDate"),
      quantity: v("quantity"),
      unitCost: v("unitCost"),
      currency,
      transferPrices,
      costAccount: v("costAccount"),
      recoveryAccount: v("recoveryAccount"),
      marginAccount: v("marginAccount"),
      matrix,
    },
  };
}

const methodLabel: Record<TransferMethod, string> = { Cost: "Cost", CostPlus: "Cost plus", Table: "Table price" };

/** Price an intercompany charge and show the legs both entities post in Journal mode (FR-40.13, BR-40.12, AC-40.11). */
export function ChargesPage() {
  const [values, setValues] = useState<ChargeValues>(ac4011);
  const { errors, failure, busy, result, run } = usePreview(previewCharge);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateCharge(values));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intercompany charges"
        description="An expense, hour, item or fee whose cost belongs to another entity is priced by the transfer price effective on its date. The borrower books the price against Due to the lender; the lender books Due from the borrower against cost recovery and the IC margin. Submitting charges from expenses and timesheets, and Invoice settlement, arrive with the posting slice."
      />
      <Card title="Charge" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="chg" fields={lineFields} values={values.line} errors={errors} onChange={(line) => setValues({ ...values, line })} />
          <h4 className={styles.section}>Transfer prices</h4>
          <RowGrid
            idPrefix="chg-price"
            path="transferPrices"
            noun="Transfer price"
            fields={priceFields}
            rows={values.prices}
            errors={errors}
            newRow={emptyPrice}
            onChange={(prices) => setValues({ ...values, prices })}
          />
          <h4 className={styles.section}>Accounting matrix</h4>
          <RowGrid
            idPrefix="chg-matrix"
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
              <Alert tone="danger" title="The charge was not priced" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Price charge
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Charge legs" columns={legColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : result ? (
        <>
          <StatGroup label="Charge pricing">
            <StatTile label="Cost" value={`${amount(result.cost)} ${result.currency}`} />
            <StatTile label="Price" value={amount(result.price)} />
            <StatTile label="IC margin" value={amount(result.margin)} />
          </StatGroup>
          <Alert tone={result.balanced ? "success" : "danger"} title={result.balanced ? "Balanced in each entity" : "Not balanced"}>
            Priced by {methodLabel[result.transferPrice.method]}
            {result.transferPrice.markupPct ? ` ${result.transferPrice.markupPct}%` : ""}
            {result.transferPrice.unitPrice ? ` at ${amount(result.transferPrice.unitPrice)}` : ""}, effective from{" "}
            {result.transferPrice.effectiveFrom}. Both entities post under pair <span className={styles.mono}>{result.pairId}</span>.
          </Alert>
          <DataTable
            label="Charge legs"
            columns={legColumns}
            rows={result.legs}
            getRowKey={(l, i) => `${l.entity}-${l.account}-${i}`}
            empty={{ title: "No legs", description: "The service returned no charge legs." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            headingLevel={3}
            title="No charge priced yet"
            description="The form is filled in with an example. Price it, or enter your own charge, transfer prices and matrix row."
          />
        </Card>
      )}
    </div>
  );
}
