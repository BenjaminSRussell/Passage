import { Badge, Button, Card, EmptyState, PageHeader, StatGroup, StatTile, StatusPill, DataTable, type DataTableColumn } from "../../shell/ui";
import styles from "./pages.module.css";

const sampleDataBadge = <Badge tone="neutral">Sample data</Badge>;

export function ProductHome({
  counts = { products: 500, masters: 50, released: 0, incomplete: 0 },
}: {
  counts?: { products: number; masters: number; released: number; incomplete: number };
}) {
  return (
    <>
      <PageHeader
        title="Product information"
        description="Product information management: products, masters and what's released to each legal entity."
        meta={sampleDataBadge}
      />
      <StatGroup label="PIM tiles">
        <StatTile label="Products" value={counts.products} />
        <StatTile label="Masters" value={counts.masters} />
        <StatTile label="Released" value={counts.released} />
        <StatTile label="Incomplete attributes" value={counts.incomplete} tone="warning" />
      </StatGroup>
    </>
  );
}

export interface Product {
  number: string;
  name: string;
  subtype: string;
  state: string;
  gtin: string;
}

export const sampleProducts: Product[] = [
  { number: "SHIRT", name: "Knit shirt", subtype: "master", state: "active", gtin: "06141411234579" },
  { number: "BOLT", name: "Hex bolt", subtype: "product", state: "active", gtin: "04006381333931" },
];

const catalogColumns: DataTableColumn<Product>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "name", header: "Name" },
  {
    key: "subtype",
    header: "Subtype",
    render: (row) => <Badge tone="neutral">{row.subtype === "master" ? "Master" : "Product"}</Badge>,
  },
  { key: "state", header: "State", render: (row) => <StatusPill status={row.state} /> },
];

export function CatalogPage({
  items = sampleProducts,
  onOpen,
}: {
  items?: Product[];
  onOpen?: (number: string) => void;
}) {
  return (
    <>
      <PageHeader title="Products" description="Every product and master in your catalog." meta={sampleDataBadge} />
      <DataTable
        label="Products"
        columns={catalogColumns}
        rows={items}
        getRowKey={(row) => row.number}
        empty={{ title: "No products yet", description: "Products you create or import appear here." }}
        onRowOpen={onOpen ? (row) => onOpen(row.number) : undefined}
      />
    </>
  );
}

export function ReleasedPage() {
  return (
    <>
      <PageHeader title="Released products" />
      <Card>
        <EmptyState
          icon="box"
          title="No released products yet"
          description="Products released to a legal entity appear here, with their item group, model group, default site and warehouse."
        />
      </Card>
    </>
  );
}

export function ProductNotFound({ onBack }: { onBack(): void }) {
  return (
    <Card>
      <EmptyState
        icon="box"
        title="Product not found"
        description="There is no product with that number. Check the address, or pick one from the catalog."
        action={<Button onClick={onBack}>Back to products</Button>}
      />
    </Card>
  );
}

export function ProductDetail({ product = sampleProducts[0] }: { product?: Product }) {
  return (
    <>
      <PageHeader title={product.name} meta={<StatusPill status="active" />} />
      <Card title="Identifiers">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Number</dt>
            <dd className={styles.value}>{product.number}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>GTIN</dt>
            <dd className={styles.value}>{product.gtin}</dd>
          </div>
        </dl>
      </Card>
    </>
  );
}
