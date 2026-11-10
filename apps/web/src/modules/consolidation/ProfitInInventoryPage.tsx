import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { previewProfitInInventory, type ProfitInInventoryPreview, type ProfitInInventoryRequest } from "./api/client";
import { Fields, type FieldSpec } from "./Fields";
import { amount, isCurrency, isDecimal } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type HeadKey = "currency" | "sellerMarginPct";
type LayerKey = "id" | "quantity" | "legalUnitCost" | "groupUnitCost";

export interface ProfitValues {
  head: Record<HeadKey, string>;
  layers: Record<LayerKey, string>[];
}

const headFields: FieldSpec<HeadKey>[] = [
  { name: "currency", label: "Receipt currency", mono: true },
  {
    name: "sellerMarginPct",
    label: "Seller's average margin (%)",
    optional: true,
    decimal: true,
    hint: "Used for layers without a group cost; required when any layer has none.",
  },
];

const layerFields: RowField<LayerKey>[] = [
  { name: "id", label: "Receipt layer", mono: true, optional: true },
  { name: "quantity", label: "Remaining quantity", decimal: true },
  { name: "legalUnitCost", label: "Legal unit cost (holder)", decimal: true },
  { name: "groupUnitCost", label: "Group unit cost (supplier)", decimal: true, optional: true },
];

/** AC-40.12: 40 units left from an IC receipt at Legal cost 120.00 and Group cost 100.00. */
export const ac4012: ProfitValues = {
  head: { currency: "EUR", sellerMarginPct: "" },
  layers: [{ id: "RCPT-0042", quantity: "40", legalUnitCost: "120.00", groupUnitCost: "100.00" }],
};

/** Checks that mirror the service, and the request the form sends. */
export function validateProfit(values: ProfitValues): Checked<ProfitInInventoryRequest> {
  const errors: Record<string, string> = {};
  const currency = values.head.currency.trim().toUpperCase();
  const margin = values.head.sellerMarginPct.trim();
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as EUR.";
  if (margin !== "" && !(isDecimal(margin) && /^0*(\d{1,2}(\.\d+)?|100(\.0+)?)$/.test(margin)))
    errors.sellerMarginPct = "Enter a margin from 0 to 100.";
  const layers = values.layers.map((l, i) => {
    const at = (k: LayerKey) => `layers[${i}].${k}`;
    const group = l.groupUnitCost.trim();
    if (!isDecimal(l.quantity)) errors[at("quantity")] = "Enter the remaining quantity, 0 or more.";
    if (!isDecimal(l.legalUnitCost)) errors[at("legalUnitCost")] = "Enter the legal unit cost, 0 or more.";
    if (group !== "" && !isDecimal(group)) errors[at("groupUnitCost")] = "Enter the group unit cost, or leave it empty.";
    if (group === "" && margin === "" && !errors.sellerMarginPct) errors.sellerMarginPct = "Enter the seller's margin for layers without a group cost.";
    return { id: l.id.trim() || undefined, quantity: l.quantity.trim(), legalUnitCost: l.legalUnitCost.trim(), groupUnitCost: group || undefined };
  });
  if (layers.length === 0) errors.layers = "Add at least one receipt layer.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { currency, sellerMarginPct: margin || undefined, layers } };
}

type LayerRow = ProfitInInventoryPreview["layers"][number];

const layerColumns: DataTableColumn<LayerRow>[] = [
  { key: "id", header: "Receipt layer", mono: true },
  {
    key: "basis",
    header: "Basis",
    render: (l) => (l.basis === "GroupCost" ? <Badge tone="success">Group cost</Badge> : <Badge tone="warning">Margin fallback</Badge>),
  },
  { key: "profit", header: "Unrealized profit", align: "end", render: (l) => amount(l.profit) },
];

/** Unrealized profit in inventory per intercompany receipt layer, eliminated by E3 (FR-40.16, BR-40.7, AC-40.12). */
export function ProfitInInventoryPage() {
  const [values, setValues] = useState<ProfitValues>(ac4012);
  const { errors, failure, busy, result, run } = usePreview(previewProfitInInventory);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateProfit(values));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Profit in inventory"
        description="Each intercompany receipt layer still held carries the supplier's margin: remaining quantity times the holder's legal unit cost less the supplier's group unit cost. A layer without a group cost falls back to the seller's average margin. Reading layers from inventory and posting E3 at the average rate arrive with consolidation runs."
      />
      <Card title="Intercompany receipt layers" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="pi" fields={headFields} values={values.head} errors={errors} onChange={(head) => setValues({ ...values, head })} />
          <RowGrid
            idPrefix="pi-layer"
            path="layers"
            noun="Layer"
            fields={layerFields}
            rows={values.layers}
            errors={errors}
            newRow={() => ({ id: "", quantity: "", legalUnitCost: "", groupUnitCost: "" })}
            onChange={(layers) => setValues({ ...values, layers })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The unrealized profit was not computed" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compute unrealized profit
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Unrealized profit by layer" columns={layerColumns} rows={[]} getRowKey={(l) => l.id} loading />
      ) : result ? (
        <>
          <StatGroup label="Unrealized profit">
            <StatTile label="To eliminate" value={`${amount(result.total)} ${result.currency}`} />
            <StatTile label="Layers on margin fallback" value={String(result.fallbackCount)} tone="warning" />
          </StatGroup>
          {result.warnings.map((w) => (
            <Alert key={w.code} tone="warning" title="Group cost unknown">
              {w.message} ({w.code})
            </Alert>
          ))}
          <DataTable label="Unrealized profit by layer" columns={layerColumns} rows={result.layers} getRowKey={(l, i) => `${l.id}-${i}`} />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="box"
            headingLevel={3}
            title="No unrealized profit yet"
            description="The form is filled in with an example. Compute it, or enter the layers your entity still holds."
          />
        </Card>
      )}
    </div>
  );
}
