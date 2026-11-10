import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { allocateVoyage, describeProblem, getVoyage, type Container, type Folio, type Leg, type Problem, type Voyage, type VoyageCostLine, type VoyageReceiptLine } from "./api/client";
import { Details } from "./Details";
import { dash, fixed, formatDateTime, money, stateTone, statusLabel, sumAmounts } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const legColumns: DataTableColumn<Leg>[] = [
  { key: "seq", header: "Leg", align: "end" },
  { key: "from", header: "From", mono: true },
  { key: "to", header: "To", mono: true },
  { key: "mode", header: "Mode" },
];

const containerColumns: DataTableColumn<Container>[] = [
  { key: "number", header: "Container", mono: true },
  { key: "type", header: "Type" },
  { key: "seal", header: "Seal", mono: true, render: (row) => row.seal || dash },
];

const folioColumns: DataTableColumn<Folio>[] = [
  { key: "folioNo", header: "Folio", mono: true },
  { key: "forwarder", header: "Vendor or forwarder" },
  { key: "containers", header: "Containers", mono: true, render: (row) => (row.containers.length ? row.containers.join(", ") : dash) },
];

const costColumns = (currency: string): DataTableColumn<VoyageCostLine>[] => [
  { key: "costType", header: "Cost type", mono: true },
  { key: "amount", header: "Amount", align: "end", render: (row) => money(row.amount, currency) },
  { key: "allocationMethod", header: "Allocated by", render: (row) => row.allocationMethod.toLowerCase() },
];

type AllocationRow = VoyageReceiptLine & { shares: Record<string, string>; total?: string };

/** One column per cost line, then the line total: the voyage cost statement per receipt line (PRD 6.34.15). */
function allocationColumns(v: Voyage): DataTableColumn<AllocationRow>[] {
  return [
    { key: "line", header: "Receipt line", align: "end" },
    { key: "value", header: "Value", align: "end", render: (row) => fixed(row.value, 2) },
    { key: "weight", header: "Weight", align: "end" },
    ...v.costAllocations.map((c, i) => ({
      key: `cost-${i}`,
      header: c.costType,
      align: "end" as const,
      render: (row: AllocationRow) => (
        <span title={row.line === c.residualLine && c.residual !== "0" ? `Includes the rounding residual ${c.residual} (TMS-0110)` : undefined}>{fixed(row.shares[i] ?? "0", 2)}</span>
      ),
    })),
    { key: "total", header: "Landed cost", align: "end", render: (row) => (row.total ? money(row.total, v.currency) : dash) },
  ];
}

function allocationRows(v: Voyage): AllocationRow[] {
  return v.receiptLines.map((r, idx) => ({
    ...r,
    shares: Object.fromEntries(v.costAllocations.map((c, i) => [i, c.shares[idx]?.amount ?? "0"])),
    total: v.allocations[idx]?.amount,
  }));
}

/** A landed-cost voyage: containers, folios, costs and, once posted, the allocation per receipt line (FR-34.9, FR-34.12). */
export function VoyageDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getVoyage(id, signal), `voyage:${id}`);
  const [posted, setPosted] = useState<Voyage | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const voyage = posted && posted.id === state.data?.id ? posted : state.data;

  async function allocate() {
    if (!voyage) return;
    setBusy(true);
    setFailure(null);
    try {
      setPosted(await allocateVoyage(voyage.id));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Voyage" />
        <Alert tone="danger" title="Could not load the voyage" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link to="/transport/voyages">Back to voyages</Link>
      </div>
    );
  }
  if (!voyage) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Voyage" />
        <Card>
          <p className={styles.note}>Loading the voyage…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Voyage ${voyage.reference}`}
        meta={<Badge tone={stateTone(voyage.status)}>{statusLabel(voyage.status)}</Badge>}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/transport/goods-in-transit?voyageId=${voyage.id}`}>
              Goods in transit
            </Link>
            {voyage.status === "ESTIMATED" ? (
              <Button variant="primary" loading={busy} onClick={allocate}>
                Allocate and post
              </Button>
            ) : null}
          </div>
        }
      />
      <Alert tone="success" live="status">
        {posted ? `Landed cost posted: ${voyage.allocations.length} receipt lines updated for costing.` : null}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="Could not allocate the voyage" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card>
        <Details
          entries={[
            { term: "Voyage", value: voyage.id, mono: true },
            { term: "Reference", value: voyage.reference, mono: true },
            { term: "Landed cost", value: money(sumAmounts(voyage.costLines.map((c) => c.amount)), voyage.currency) },
            { term: "Created", value: formatDateTime(voyage.createdAt) },
            { term: "Posted", value: formatDateTime(voyage.postedAt) },
          ]}
        />
      </Card>
      <Card title="Legs" headingLevel={3}>
        <DataTable label="Legs" columns={legColumns} rows={voyage.legs} getRowKey={(row, i) => `${row.seq ?? i}`} />
      </Card>
      <Card title="Containers and folios" headingLevel={3}>
        <div className={styles.stack}>
          <DataTable label="Containers" columns={containerColumns} rows={voyage.containers} getRowKey={(row) => row.number} empty={{ title: "No containers" }} />
          <DataTable label="Folios" columns={folioColumns} rows={voyage.folios} getRowKey={(row) => row.folioNo} empty={{ title: "No folios" }} />
        </div>
      </Card>
      <Card title="Landed costs" headingLevel={3}>
        <DataTable label="Landed costs" columns={costColumns(voyage.currency)} rows={voyage.costLines} getRowKey={(row, i) => `${row.costType}-${i}`} />
      </Card>
      <Card title={voyage.status === "POSTED" ? "Allocation per receipt line" : "Receipt lines"} headingLevel={3}>
        <div className={styles.stack}>
          {voyage.status === "ESTIMATED" ? <p className={styles.note}>Allocate the voyage to see each receipt line's share of every cost.</p> : null}
          <DataTable label="Receipt line allocation" columns={allocationColumns(voyage)} rows={allocationRows(voyage)} getRowKey={(row) => String(row.line)} />
        </div>
      </Card>
      <Link to="/transport/voyages">Back to voyages</Link>
    </div>
  );
}
