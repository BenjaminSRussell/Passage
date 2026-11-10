import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Badge, Button, Card, DataTable, EmptyState, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listProducts, ProductApiError, type Product } from "./api/client";
import { DUTY_WRITE, productTypeLabel, subtypeLabel, trackingFlags, trackingLabel } from "./labels";
import styles from "./pages.module.css";
import { ProductProblem } from "./ProductProblem";
import { useItemGroups, useProduct } from "./useProduct";

const PAGE_SIZE = 50;

export function ProductHome() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_WRITE);
  return (
    <>
      <PageHeader
        title="Product information"
        description="Product information management: products, masters and what's released to each legal entity."
      />
      <Card>
        <EmptyState
          icon="box"
          title="Find or create a product"
          description="Search the catalog by number or name, or add a product so it can be used on purchase and sales order lines."
          action={
            <>
              <Button onClick={() => navigate("/product/items")}>Open the catalog</Button>
              {canWrite ? (
                <Button variant="primary" onClick={() => navigate("/product/items/new")}>
                  New product
                </Button>
              ) : null}
            </>
          }
        />
      </Card>
    </>
  );
}

const catalogColumns: DataTableColumn<Product>[] = [
  { key: "productNumber", header: "Number", mono: true },
  { key: "name", header: "Name" },
  { key: "subtype", header: "Kind", render: (row) => <Badge tone="neutral">{subtypeLabel[row.subtype]}</Badge> },
  { key: "baseUom", header: "Unit", mono: true },
  { key: "lifecycleState", header: "State", render: (row) => <StatusPill status={row.lifecycleState} /> },
];

interface Listing {
  rows: Product[];
  next?: string;
  loading: boolean;
  loadingMore: boolean;
  error: unknown;
}

export function CatalogPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_WRITE);
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [listing, setListing] = useState<Listing>({ rows: [], loading: true, loadingMore: false, error: null });
  const latest = useRef(0);

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setListing({ rows: [], loading: true, loadingMore: false, error: null });
    listProducts({ q: query, limit: PAGE_SIZE }, controller.signal).then(
      (page) => token === latest.current && setListing({ rows: page.items, next: page.nextCursor, loading: false, loadingMore: false, error: null }),
      (error: unknown) => token === latest.current && !controller.signal.aborted && setListing({ rows: [], loading: false, loadingMore: false, error }),
    );
    return () => controller.abort();
  }, [query, attempt]);

  function loadMore() {
    const token = ++latest.current;
    setListing((prev) => ({ ...prev, loadingMore: true, error: null }));
    listProducts({ q: query, cursor: listing.next, limit: PAGE_SIZE }).then(
      (page) =>
        token === latest.current &&
        setListing((prev) => ({ rows: [...prev.rows, ...page.items], next: page.nextCursor, loading: false, loadingMore: false, error: null })),
      (error: unknown) => token === latest.current && setListing((prev) => ({ ...prev, loadingMore: false, error })),
    );
  }

  function search(event: FormEvent) {
    event.preventDefault();
    setQuery(draft.trim());
  }

  const searching = query !== "";
  const empty = searching
    ? { title: "No products match your search", description: "Check the spelling, or search by the product number or part of the name." }
    : { title: "No products yet", description: canWrite ? "Use New product to add the first one." : "Products that are added appear here." };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Products"
        description="Every product and master in your catalog."
        actions={
          canWrite ? (
            <Button variant="primary" onClick={() => navigate("/product/items/new")}>
              New product
            </Button>
          ) : undefined
        }
      />
      <form className={styles.toolbar} role="search" aria-label="Search products" onSubmit={search}>
        <Input
          type="search"
          aria-label="Search by number or name"
          placeholder="Search by number or name"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
        <Button type="submit">Search</Button>
      </form>
      {listing.error && listing.rows.length === 0 ? (
        <ProductProblem error={listing.error} title="Could not load the products" retry={() => setAttempt((n) => n + 1)} />
      ) : (
        <>
          <DataTable
            label="Products"
            columns={catalogColumns}
            rows={listing.rows}
            getRowKey={(row) => row.id}
            loading={listing.loading}
            empty={empty}
            onRowOpen={(row) => navigate(`/product/${encodeURIComponent(row.id)}`)}
          />
          {listing.error ? <ProductProblem error={listing.error} title="Could not load more products" retry={loadMore} /> : null}
          {listing.next && !listing.error ? (
            <div>
              <Button onClick={loadMore} loading={listing.loadingMore}>
                Show more products
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
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
        description="There is no product at this address. Check the link, or pick one from the catalog."
        action={<Button onClick={onBack}>Back to products</Button>}
      />
    </Card>
  );
}

function Entry({ term, children, mono }: { term: string; children: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children || "None"}</dd>
    </div>
  );
}

function ItemGroupEntry({ id }: { id: string }) {
  const { groups, failed } = useItemGroups();
  return <Entry term="Item group">{groups.find((group) => group.id === id)?.name ?? (failed ? "Not available" : "")}</Entry>;
}

export function ProductDetail() {
  const { id } = useParams();
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_WRITE);
  const { data, error, loading, reload } = useProduct(id);

  if (loading) {
    return (
      <>
        <PageHeader title="Product" />
        <Card>
          <Skeleton width="3/4" />
        </Card>
        <span className="visually-hidden" role="status">
          Loading the product…
        </span>
      </>
    );
  }
  if (error instanceof ProductApiError && error.status === 404) {
    return <ProductNotFound onBack={() => navigate("/product/items")} />;
  }
  if (error || !data) {
    return <ProductProblem error={error} title="Could not load the product" retry={reload} />;
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title={data.name}
        meta={<StatusPill status={data.lifecycleState} />}
        actions={
          canWrite ? (
            <Button onClick={() => navigate(`/product/${encodeURIComponent(data.id)}/edit`)}>Edit product</Button>
          ) : undefined
        }
      />
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Number" mono>
            {data.productNumber}
          </Entry>
          <Entry term="Kind">{subtypeLabel[data.subtype]}</Entry>
          <Entry term="Type">{productTypeLabel[data.productType]}</Entry>
          <Entry term="Base unit" mono>
            {data.baseUom}
          </Entry>
          {data.itemGroupId !== "" ? <ItemGroupEntry id={data.itemGroupId} /> : <Entry term="Item group">{""}</Entry>}
          <Entry term="Tracking">{trackingFlags.filter((flag) => data[flag]).map((flag) => trackingLabel[flag]).join(", ")}</Entry>
          <Entry term="Description">{data.description}</Entry>
        </dl>
      </Card>
    </div>
  );
}
