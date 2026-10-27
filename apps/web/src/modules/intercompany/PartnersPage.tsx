import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { listPartners, type Partner, type PartnerQuery } from "./api/partners";
import { pricingLabels } from "./PartnerFormPage";
import styles from "./pages.module.css";

/** "±0.5 % price, ±2 days" */
export function toleranceText(p: Partner): string {
  return `±${p.tolerance.pricePct} % price, ±${p.tolerance.dateDays} ${p.tolerance.dateDays === 1 ? "day" : "days"}`;
}

export function pricingText(p: Partner): string {
  return p.pricing === "CostPlus" ? `${pricingLabels.CostPlus} ${p.markupPct} %` : pricingLabels[p.pricing];
}

const columns: DataTableColumn<Partner>[] = [
  { key: "direction", header: "Seller to buyer", mono: true, render: (p) => `${p.sellerEntity} → ${p.buyerEntity}` },
  { key: "accounts", header: "Customer / vendor", mono: true, render: (p) => `${p.customerAccount} / ${p.vendorAccount}` },
  { key: "pricing", header: "Pricing", render: pricingText },
  { key: "tolerance", header: "Change tolerance", render: toleranceText },
  { key: "status", header: "Status", render: (p) => <StatusPill status={p.active ? "active" : "inactive"} /> },
  { key: "version", header: "Version", align: "end", render: (p) => String(p.version) },
];

type Show = "all" | "active" | "inactive";

interface ListState {
  loading: boolean;
  items: Partner[];
  next?: string;
  failure: Problem | null;
}

/** The tenant's trading-partner pairings (FR-40.1). */
export function PartnersPage() {
  const navigate = useNavigate();
  const [entity, setEntity] = useState("");
  const [show, setShow] = useState<Show>("all");
  const [query, setQuery] = useState<PartnerQuery>({});
  const [reload, setReload] = useState(0);
  const [state, setState] = useState<ListState>({ loading: true, items: [], failure: null });
  const [more, setMore] = useState(false);

  useEffect(() => {
    const ctl = new AbortController();
    setState((s) => ({ ...s, loading: true, failure: null }));
    listPartners(query, ctl.signal)
      .then((page) => setState({ loading: false, items: page.items, next: page.nextCursor, failure: null }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setState({ loading: false, items: [], failure: describeProblem(err) });
      });
    return () => ctl.abort();
  }, [query, reload]);

  function applyFilter(event: FormEvent) {
    event.preventDefault();
    setQuery({ entity: entity.trim() || undefined, active: show === "all" ? undefined : show === "active" });
  }

  async function loadMore() {
    if (!state.next) return;
    setMore(true);
    try {
      const page = await listPartners({ ...query, after: state.next });
      setState((s) => ({ ...s, items: [...s.items, ...page.items], next: page.nextCursor }));
    } catch (err) {
      setState((s) => ({ ...s, failure: describeProblem(err) }));
    } finally {
      setMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Trading partners"
        description="Each pairing links a customer in the seller entity with a vendor in the buyer entity, with the actions, pricing and change tolerance the mirror documents follow. Every policy change is a new version."
        actions={
          <Link className={buttonClassName("primary")} to="/intercompany/partners/new">
            New trading partner
          </Link>
        }
      />
      <Card>
        <form className={styles.filters} onSubmit={applyFilter} noValidate>
          <FormField id="partners-entity" label="Entity" optional hint="Either side of the pairing.">
            {(control) => <Input {...control} className={styles.mono} value={entity} onChange={(e) => setEntity(e.target.value)} />}
          </FormField>
          <FormField id="partners-show" label="Show">
            {(control) => (
              <Select {...control} value={show} onChange={(e) => setShow(e.target.value as Show)}>
                <option value="all">All pairings</option>
                <option value="active">Active only</option>
                <option value="inactive">Inactive only</option>
              </Select>
            )}
          </FormField>
          <Button type="submit">Apply filter</Button>
        </form>
      </Card>
      {state.failure ? (
        <Alert
          tone="danger"
          title="We couldn't load the trading partners"
          live="alert"
          action={
            <Button size="sm" onClick={() => setReload((n) => n + 1)}>
              Try again
            </Button>
          }
        >
          {state.failure.message} ({state.failure.code})
        </Alert>
      ) : (
        <DataTable
          label="Trading partners"
          columns={columns}
          rows={state.items}
          getRowKey={(p) => p.id}
          loading={state.loading}
          onRowOpen={(p) => navigate(`/intercompany/partners/${p.id}`)}
          empty={
            query.entity || query.active !== undefined
              ? { title: "No pairings match", description: "Try another entity or show all pairings." }
              : { title: "No trading partners yet", description: "Pair a customer in one entity with a vendor in another to trade between them." }
          }
        />
      )}
      {state.next && !state.failure ? (
        <div className={styles.formActions}>
          <Button loading={more} onClick={() => void loadMore()}>
            Show more pairings
          </Button>
        </div>
      ) : null}
    </div>
  );
}
