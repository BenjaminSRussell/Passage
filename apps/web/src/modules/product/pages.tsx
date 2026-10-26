import { Badge, Card, EmptyState, PageHeader, StatGroup, StatTile, StatusPill, DataTable, type DataTableColumn } from "../../shell/ui";
import styles from "./pages.module.css";

export function ProductHome({
  counts = { products: 500, masters: 50, released: 500, incomplete: 0 },
}: {
  counts?: { products: number; masters: number; released: number; incomplete: number };
}) {
  return (
    <>
      <PageHeader title="Product information management" description="Products, masters and what's released to each legal entity." />
      <StatGroup label="PIM tiles">
        <StatTile label="Products" value={counts.products} />
        <StatTile label="Masters" value={counts.masters} />
        <StatTile label="Released" value={counts.released} />
        <StatTile label="Incomplete attributes" value={counts.incomplete} tone="warning" />
      </StatGroup>
    </>
  );
}

interface CatalogItem {
  number: string;
  name: string;
  subtype: string;
  state: string;
}

const catalogColumns: DataTableColumn<CatalogItem>[] = [
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
  items = [
    { number: "SHIRT", name: "Knit shirt", subtype: "master", state: "active" },
    { number: "BOLT", name: "Hex bolt", subtype: "product", state: "active" },
  ],
  onOpen,
}: {
  items?: CatalogItem[];
  onOpen?: (number: string) => void;
}) {
  return (
    <>
      <PageHeader title="All products and masters" />
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
          description="Item group, model group, default site and warehouse per legal entity."
        />
      </Card>
    </>
  );
}

export function ProductDetail({
  product = { number: "SHIRT", name: "Knit shirt", gtin: "06141411234579" },
}: {
  product?: { number: string; name: string; gtin: string };
}) {
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
