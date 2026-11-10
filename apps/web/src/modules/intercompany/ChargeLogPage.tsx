import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { listCharges, type ChargeRecord, type ChargeStatus } from "./api/postings";
import { amount } from "./format";
import styles from "./pages.module.css";

const tone: Record<ChargeStatus, BadgeTone> = { posted: "success", pending: "warning", suspense: "warning", compensated: "danger" };
const label: Record<ChargeStatus, string> = { posted: "Posted", pending: "Paused, can be resumed", suspense: "On suspense: no transfer price", compensated: "Reversed" };

const columns: DataTableColumn<ChargeRecord>[] = [
  { key: "source", header: "Source", mono: true, render: (c) => `${c.sourceId} v${c.version}` },
  { key: "pair", header: "Lender to borrower", mono: true, render: (c) => `${c.lendingEntity} → ${c.borrowingEntity}` },
  { key: "category", header: "Category", render: (c) => (c.resourceRole ? `${c.category}, ${c.resourceRole}` : c.category) },
  { key: "date", header: "Transaction date", mono: true, render: (c) => c.transactionDate },
  { key: "cost", header: "Cost", align: "end", render: (c) => (c.cost ? `${amount(c.cost)} ${c.currency}` : "—") },
  { key: "price", header: "Price", align: "end", render: (c) => (c.price ? `${amount(c.price)} ${c.currency}` : "—") },
  { key: "status", header: "Status", render: (c) => <Badge tone={tone[c.status]}>{label[c.status]}</Badge> },
];

/** Every IC charge submitted, per source and version (FR-40.13, BR-40.12). */
export function ChargeLogPage() {
  const [status, setStatus] = useState("");
  const [state, setState] = useState<{ loading: boolean; items: ChargeRecord[]; next?: string; failure: Problem | null }>({ loading: true, items: [], failure: null });
  const [reload, setReload] = useState(0);
  const [more, setMore] = useState(false);

  useEffect(() => {
    const ctl = new AbortController();
    setState((s) => ({ ...s, loading: true, failure: null }));
    listCharges({ status: status || undefined }, ctl.signal)
      .then((page) => setState({ loading: false, items: page.items, next: page.nextCursor, failure: null }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setState({ loading: false, items: [], failure: describeProblem(err) });
      });
    return () => ctl.abort();
  }, [status, reload]);

  async function loadMore() {
    if (!state.next) return;
    setMore(true);
    try {
      const page = await listCharges({ status: status || undefined, after: state.next });
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
        title="Intercompany charge log"
        description="A charge on suspense had no transfer price on its date, so nothing was posted. Add the price and submit the source again with the next version to post it."
        actions={<Link to="/intercompany/charges">Price and post a charge</Link>}
      />
      <Card>
        <div className={styles.filters}>
          <FormField id="charges-status" label="Show">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="">All charges</option>
                <option value="posted">Posted</option>
                <option value="suspense">On suspense</option>
                <option value="compensated">Reversed</option>
                <option value="pending">Paused</option>
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {state.failure ? (
        <Alert tone="danger" title="We couldn't load the charges" live="alert" action={<Button size="sm" onClick={() => setReload((n) => n + 1)}>Try again</Button>}>
          <ProblemMessage problem={state.failure} />
        </Alert>
      ) : (
        <DataTable label="Intercompany charges" columns={columns} rows={state.items} getRowKey={(c) => c.id} loading={state.loading} empty={{ title: "No charges yet", description: "Price a charge and post it to see it here." }} />
      )}
      {state.next && !state.failure ? (
        <div className={styles.formActions}>
          <Button loading={more} onClick={() => void loadMore()}>
            Show more charges
          </Button>
        </div>
      ) : null}
    </div>
  );
}
