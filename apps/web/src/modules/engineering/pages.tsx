import { useState, type FormEvent } from "react";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  type DataTableColumn,
} from "../../shell/ui";
import { whereUsed, type UsedBy } from "./api/client";
import styles from "./pages.module.css";

export function EngineeringHome({
  onOpenWhereUsed,
  onOpenRoutes,
  onOpenResources,
  onOpenResourceGroups,
  onOpenCapabilities,
}: {
  onOpenWhereUsed?: () => void;
  onOpenRoutes?: () => void;
  onOpenResources?: () => void;
  onOpenResourceGroups?: () => void;
  onOpenCapabilities?: () => void;
}) {
  const resourceCards = [
    { title: "Resources", text: "Machines, tools, labor, vendors and locations, with their capabilities and calendars.", open: onOpenResources },
    { title: "Resource groups", text: "Groups of resources by site, each linking to its members.", open: onOpenResourceGroups },
    { title: "Capabilities", text: "Skills and abilities, each linking to the resources that hold it.", open: onOpenCapabilities },
  ];
  return (
    <>
      <PageHeader title="Engineering data" description="Bills of material, routings and change orders." />
      <Card
        title="Routings"
        actions={
          onOpenRoutes ? (
            <Button size="sm" onClick={onOpenRoutes}>
              Open Routings
            </Button>
          ) : undefined
        }
      >
        <p className={styles.lead}>Browse route versions, their operations and resource requirements, and estimate lead time.</p>
      </Card>
      {resourceCards.map(({ title, text, open }) => (
        <Card
          key={title}
          title={title}
          actions={
            open ? (
              <Button size="sm" onClick={open}>
                Open {title}
              </Button>
            ) : undefined
          }
        >
          <p className={styles.lead}>{text}</p>
        </Card>
      ))}
      <Card
        title="Where-used"
        actions={
          onOpenWhereUsed ? (
            <Button size="sm" onClick={onOpenWhereUsed}>
              Open Where-used
            </Button>
          ) : undefined
        }
      >
        <p className={styles.lead}>
          Find every product that uses a component, across all BOM levels, as of a date.
        </p>
      </Card>
    </>
  );
}

function flatten(nodes: UsedBy[]): UsedBy[] {
  return nodes.flatMap((n) => [n, ...flatten(n.parents ?? [])]);
}

const columns: DataTableColumn<UsedBy>[] = [
  {
    key: "productId",
    header: "Product",
    mono: true,
    render: (row) => (
      <span className={styles.product}>
        {Array.from({ length: Math.max(row.level - 1, 0) }, (_, i) => (
          <span key={i} className={styles.indent} aria-hidden="true" />
        ))}
        {row.productId}
      </span>
    ),
  },
  { key: "level", header: "Level", align: "end" },
  { key: "versionId", header: "BOM version", mono: true },
  { key: "siteId", header: "Site" },
];

export function WhereUsedPage({ initialComponent = "" }: { initialComponent?: string }) {
  const [component, setComponent] = useState(initialComponent);
  const [asOf, setAsOf] = useState("");
  const [rows, setRows] = useState<UsedBy[] | null>(null);
  const [error, setError] = useState("");
  const [searching, setSearching] = useState(false);

  async function search(event: FormEvent) {
    event.preventDefault();
    setError("");
    setSearching(true);
    try {
      setRows(flatten(await whereUsed(component.trim(), asOf)));
    } catch {
      setRows(null);
      setError("Where-used lookup failed. Check the component and date.");
    } finally {
      setSearching(false);
    }
  }

  function statusText(): string {
    if (searching) return "Searching…";
    if (rows === null) return "";
    if (rows.length === 0) return "No active BOM uses this component on that date.";
    return `${rows.length} ${rows.length === 1 ? "parent" : "parents"} found`;
  }

  return (
    <>
      <PageHeader title="Where-used" description="Find every product that uses a component, as of a date." />
      <Card>
        <form className={styles.form} onSubmit={search}>
          <div className={styles.field}>
            <FormField id="where-used-component" label="Component">
              {(control) => (
                <Input {...control} value={component} onChange={(e) => setComponent(e.target.value)} required />
              )}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="where-used-as-of" label="As of date" optional>
              {(control) => <Input {...control} type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />}
            </FormField>
          </div>
          <Button type="submit" variant="primary" loading={searching}>
            Find parents
          </Button>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
      <p role="status" className={styles.summary}>
        {statusText()}
      </p>
      <div className={styles.results}>
        {searching || (rows && rows.length > 0) ? (
          <DataTable
            label={searching ? "Searching for parents" : "Where-used results"}
            columns={columns}
            rows={rows ?? []}
            loading={searching}
            getRowKey={(row, index) => `${index}-${row.level}-${row.versionId}-${row.productId}`}
          />
        ) : null}
        {!searching && rows && rows.length === 0 ? (
          <Card>
            <EmptyState compact title="No parents found" description="Try another component or a different date." />
          </Card>
        ) : null}
      </div>
    </>
  );
}
