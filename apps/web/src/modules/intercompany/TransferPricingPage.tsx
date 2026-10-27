import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, PageHeader, type DataTableColumn } from "../../shell/ui";
import { exportEvidence, type EvidenceRequest, type EvidenceRow } from "./api/client";
import { Fields, type FieldSpec } from "./Fields";
import { amount, isCurrency, isDecimal, isPositive } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type HeadKey = "sellerEntity" | "buyerEntity" | "period" | "currency";
type RowKey = keyof EvidenceRow;

export interface EvidenceValues {
  head: Record<HeadKey, string>;
  rows: Record<RowKey, string>[];
}

const headFields: FieldSpec<HeadKey>[] = [
  { name: "sellerEntity", label: "Seller entity", mono: true },
  { name: "buyerEntity", label: "Buyer entity", mono: true },
  { name: "period", label: "Period", mono: true, hint: "Such as 2026-11." },
  { name: "currency", label: "Currency", mono: true },
];

const rowFields: RowField<RowKey>[] = [
  { name: "item", label: "Item or category", mono: true },
  { name: "quantity", label: "Quantity", decimal: true },
  { name: "price", label: "Price (line total)", decimal: true },
  { name: "cost", label: "Cost (line total)", decimal: true },
  { name: "basis", label: "Pricing basis", mono: true },
];

const emptyRow = (): Record<RowKey, string> => ({ item: "", quantity: "", price: "", cost: "", basis: "" });

/** The AC-40.11 consulting charge and an IC sale priced at cost, P to S in November 2026. */
export const sampleEvidence: EvidenceValues = {
  head: { sellerEntity: "P", buyerEntity: "S", period: "2026-11", currency: "EUR" },
  rows: [
    { item: "Hour/Consultant", quantity: "8", price: "528.00", cost: "480.00", basis: "CostPlus" },
    { item: "PUMP-100", quantity: "10", price: "1500.00", cost: "1500.00", basis: "Cost" },
  ],
};

/** Checks that mirror the service, and the request the form sends. */
export function validateEvidence(values: EvidenceValues): Checked<EvidenceRequest> {
  const errors: Record<string, string> = {};
  const h = (k: HeadKey) => values.head[k].trim();
  if (h("sellerEntity") === "") errors.sellerEntity = "Enter the seller entity.";
  if (h("buyerEntity") === "") errors.buyerEntity = "Enter the buyer entity.";
  else if (h("buyerEntity") === h("sellerEntity")) errors.buyerEntity = "The buyer must differ from the seller.";
  if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(h("period"))) errors.period = "Enter a period such as 2026-11.";
  const currency = h("currency").toUpperCase();
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as EUR.";
  if (values.rows.length === 0) errors.rows = "Add at least one line.";
  const rows = values.rows.map((row, i) => {
    const v = (k: RowKey) => row[k].trim();
    const at = (k: RowKey) => `rows[${i}].${k}`;
    if (v("item") === "") errors[at("item")] = "Enter the item or category.";
    if (!isPositive(v("quantity"))) errors[at("quantity")] = "Enter a quantity above 0.";
    if (!isDecimal(v("price"))) errors[at("price")] = "Enter the price, 0 or more.";
    if (!isDecimal(v("cost"))) errors[at("cost")] = "Enter the cost, 0 or more.";
    if (v("basis") === "") errors[at("basis")] = "Enter the pricing basis.";
    return { item: v("item"), quantity: v("quantity"), price: v("price"), cost: v("cost"), basis: v("basis") };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { sellerEntity: h("sellerEntity"), buyerEntity: h("buyerEntity"), period: h("period"), currency, rows } };
}

/** Splits CSV text into rows of cells, honouring quoted cells with commas, quotes and line breaks. */
export function parseCsv(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (c === '"') quoted = false;
      else cell += c;
    } else if (c === '"') quoted = true;
    else if (c === ",") {
      row.push(cell);
      cell = "";
    } else if (c === "\n" || c === "\r") {
      if (c === "\r" && text[i + 1] === "\n") i++;
      row.push(cell);
      rows.push(row);
      row = [];
      cell = "";
    } else cell += c;
  }
  if (cell !== "" || row.length > 0) {
    row.push(cell);
    rows.push(row);
  }
  return rows;
}

type Line = Record<string, string>;

const lineColumns: DataTableColumn<Line>[] = [
  { key: "item", header: "Item", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "price", header: "Price", align: "end", render: (l) => amount(l.price, 0) },
  { key: "cost", header: "Cost", align: "end", render: (l) => amount(l.cost, 0) },
  { key: "margin", header: "Margin", align: "end", render: (l) => amount(l.margin, 0) },
  { key: "margin_pct", header: "Margin %", align: "end" },
  { key: "basis", header: "Basis", mono: true },
];

/** Export the transfer-pricing evidence file for one entity pair and period (FR-40.4). */
export function TransferPricingPage() {
  const [values, setValues] = useState<EvidenceValues>(sampleEvidence);
  const { errors, failure, busy, result, run } = usePreview(exportEvidence);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateEvidence(values));
  }

  const [header, ...body] = result ? parseCsv(result.csv) : [[]];
  const lines: Line[] = body.map((cells) => Object.fromEntries(header.map((name, i) => [name, cells[i] ?? ""])));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Transfer-pricing evidence"
        description="The evidence file lists each priced intercompany line between two entities for a period: quantity, price, cost, margin and the pricing basis. Passage documents the prices; it does not compute arm's-length ranges. Reading the lines from stored links and charges arrives with the persistence slice."
      />
      <Card title="Entity pair and lines" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="tp" fields={headFields} values={values.head} errors={errors} onChange={(head) => setValues({ ...values, head })} />
          <h4 className={styles.section}>Priced lines</h4>
          <RowGrid
            idPrefix="tp-row"
            path="rows"
            noun="Line"
            fields={rowFields}
            rows={values.rows}
            errors={errors}
            newRow={emptyRow}
            onChange={(rows) => setValues({ ...values, rows })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The evidence file was not produced" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Produce evidence file
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Evidence lines" columns={lineColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : result ? (
        <>
          <Alert tone="success" title="Evidence file ready">
            <a className={styles.link} href={`data:text/csv;charset=utf-8,${encodeURIComponent(result.csv)}`} download={result.fileName}>
              Download {result.fileName}
            </a>
          </Alert>
          <DataTable
            label="Evidence lines"
            columns={lineColumns}
            rows={lines}
            getRowKey={(l, i) => `${l.item}-${i}`}
            empty={{ title: "No lines", description: "The evidence file has no lines." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="note"
            headingLevel={3}
            title="No evidence file yet"
            description="The form holds the November 2026 lines from P to S. Produce the file, or enter your own lines."
          />
        </Card>
      )}
    </div>
  );
}
