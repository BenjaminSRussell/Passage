import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, PageHeader, type DataTableColumn } from "../../shell/ui";
import { previewChain, type ChainLeg, type ChainPreview, type ChainPreviewRequest } from "./api/client";
import { Fields, type FieldSpec } from "./Fields";
import { isPositive } from "./format";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type HeadKey = "chainId" | "sellerEntity" | "buyerEntity" | "active" | "shippedQuantity";
type LegName = "customerOrder" | "icPurchase" | "icSales";
type LegKey = `${LegName}.${keyof ChainLeg}`;
type Key = HeadKey | LegKey;

const headFields: FieldSpec<Key>[] = [
  { name: "chainId", label: "Chain", mono: true },
  { name: "sellerEntity", label: "Seller entity", path: "partner.sellerEntity", mono: true, hint: "Ships the goods on its IC sales order." },
  { name: "buyerEntity", label: "Buyer entity", path: "partner.buyerEntity", mono: true, hint: "Sells to its customer and buys from the seller." },
  {
    name: "active",
    label: "Partner pairing",
    options: [
      { value: "yes", label: "Active" },
      { value: "no", label: "Inactive" },
    ],
  },
  { name: "shippedQuantity", label: "Shipped quantity", decimal: true, optional: true, hint: "Defaults to the IC sales order quantity." },
];

const legLabels: Record<LegName, string> = {
  customerOrder: "Customer order (buyer)",
  icPurchase: "IC purchase order (buyer)",
  icSales: "IC sales order (seller)",
};

const legFields = (leg: LegName): FieldSpec<Key>[] => [
  { name: `${leg}.docId`, label: `${legLabels[leg]} number`, mono: true },
  { name: `${leg}.entity`, label: `${legLabels[leg]} entity`, mono: true },
  { name: `${leg}.quantity`, label: `${legLabels[leg]} quantity`, decimal: true },
];

const legs: LegName[] = ["customerOrder", "icPurchase", "icSales"];

/** AC-40.3: A sells 20 pumps to its customer and buys them from B, which ships directly. */
export const ac403: Record<Key, string> = {
  chainId: "CH-2026-0001",
  sellerEntity: "B",
  buyerEntity: "A",
  active: "yes",
  shippedQuantity: "",
  "customerOrder.docId": "SO-A-1001",
  "customerOrder.entity": "A",
  "customerOrder.quantity": "20",
  "icPurchase.docId": "PO-A-2001",
  "icPurchase.entity": "A",
  "icPurchase.quantity": "20",
  "icSales.docId": "SO-B-3001",
  "icSales.entity": "B",
  "icSales.quantity": "20",
};

/** Checks that mirror the service, and the request the form sends. */
export function validateChain(values: Record<Key, string>): Checked<ChainPreviewRequest> {
  const errors: Record<string, string> = {};
  const v = (k: Key) => values[k].trim();
  if (v("chainId") === "") errors.chainId = "Enter the chain.";
  if (v("sellerEntity") === "") errors["partner.sellerEntity"] = "Enter the seller entity.";
  if (v("buyerEntity") === "") errors["partner.buyerEntity"] = "Enter the buyer entity.";
  else if (v("buyerEntity") === v("sellerEntity")) errors["partner.buyerEntity"] = "The buyer must differ from the seller.";
  const leg = (name: LegName): ChainLeg => {
    if (v(`${name}.docId`) === "") errors[`${name}.docId`] = "Enter the document number.";
    if (v(`${name}.entity`) === "") errors[`${name}.entity`] = "Enter the entity.";
    if (!isPositive(v(`${name}.quantity`))) errors[`${name}.quantity`] = "Enter a quantity above 0.";
    return { docId: v(`${name}.docId`), entity: v(`${name}.entity`), quantity: v(`${name}.quantity`) };
  };
  const [customerOrder, icPurchase, icSales] = legs.map(leg);
  if (v("shippedQuantity") !== "" && !isPositive(v("shippedQuantity"))) errors.shippedQuantity = "Enter a quantity above 0, or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      chainId: v("chainId"),
      partner: { sellerEntity: v("sellerEntity"), buyerEntity: v("buyerEntity"), active: v("active") === "yes" },
      customerOrder,
      icPurchase,
      icSales,
      shippedQuantity: v("shippedQuantity") || undefined,
    },
  };
}

type Posting = ChainPreview["postings"][number];

const kindLabels: Record<Posting["kind"], string> = {
  ProductReceipt: "Product receipt",
  CustomerPackingSlip: "Customer packing slip",
};

const postingColumns: DataTableColumn<Posting>[] = [
  { key: "kind", header: "Posts", render: (p) => <Badge tone="info">{kindLabels[p.kind] ?? p.kind}</Badge> },
  { key: "entity", header: "Entity", mono: true },
  { key: "docId", header: "Against document", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
];

/** Check a direct delivery chain and see what the seller's packing slip posts in the buyer entity (FR-40.3, AC-40.3). */
export function ChainsPage() {
  const [values, setValues] = useState<Record<Key, string>>(ac403);
  const { errors, failure, busy, result, run } = usePreview(previewChain);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateChain(values));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Direct delivery chains"
        description="A customer order in the buyer entity, its intercompany purchase order and the seller's intercompany sales order that ships straight to the customer. When the seller posts the packing slip, the buyer's product receipt and customer packing slip post in the same saga at the shipped quantity. Stored chains and the chain view arrive with the mirror-document slice."
      />
      <Card title="Chain" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="chain" fields={headFields} values={values} errors={errors} onChange={setValues} />
          <div className={styles.lines}>
            {legs.map((leg) => (
              <fieldset key={leg} className={styles.lineCard}>
                <legend className={styles.lineLegend}>{legLabels[leg]}</legend>
                <Fields idPrefix="chain" fields={legFields(leg)} values={values} errors={errors} onChange={setValues} />
              </fieldset>
            ))}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The chain was not accepted" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview packing slip postings
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Packing slip postings" columns={postingColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : result ? (
        <>
          <Alert tone="success" title="Chain is consistent">
            The packing slip for chain <span className={styles.mono}>{result.chainId}</span> posts both documents in one saga.
          </Alert>
          <DataTable
            label="Packing slip postings"
            columns={postingColumns}
            rows={result.postings}
            getRowKey={(p) => `${p.kind}-${p.docId}`}
            empty={{ title: "No postings", description: "The service returned no postings for this chain." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="workflow"
            headingLevel={3}
            title="No chain checked yet"
            description="The form holds acceptance example AC-40.3. Preview it, or enter your own chain."
          />
        </Card>
      )}
    </div>
  );
}
