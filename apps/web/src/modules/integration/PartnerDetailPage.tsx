import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getPartner, listInterchanges, type InterchangeSummary, type Partner } from "./api/client";
import { formatDateTime } from "./format";
import { AckPill } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<InterchangeSummary>[] = [
  { key: "controlNumber", header: "ISA13", mono: true },
  { key: "direction", header: "Direction", render: (row) => (row.direction === "IN" ? "Inbound" : "Outbound") },
  { key: "receivedAt", header: "Recorded", render: (row) => formatDateTime(row.receivedAt) },
  { key: "messageCount", header: "Transactions", align: "end", render: (row) => String(row.messageCount) },
  { key: "ackStatus", header: "997", render: (row) => <AckPill status={row.ackStatus} /> },
];

/** One partner profile and its most recent interchanges. */
export function PartnerDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const partner = useLoad<Partner>((signal) => getPartner(id, signal), `partner:${id}`);
  const recent = useLoad((signal) => listInterchanges({ partnerId: id, limit: 10 }, signal), `partner-interchanges:${id}`);

  if (partner.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Trading partner" />
        <Alert tone="danger" title="Could not load the partner" live="alert" action={<Button size="sm" onClick={partner.reload}>Try again</Button>}>
          {partner.error.message} ({partner.error.code})
        </Alert>
      </div>
    );
  }
  if (!partner.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Trading partner" />
        <span className="visually-hidden">Loading the partner…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const p = partner.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={p.name}
        description={`ISA ${p.isaQualifier}/${p.isaId} in ${p.environment}`}
        meta={<StatusPill status={p.status} />}
        actions={
          <>
            {p.status === "active" ? (
              <Link className={buttonClassName("primary")} to={`/integration/interchanges/outbound?partnerId=${encodeURIComponent(p.id)}`}>
                Send document
              </Link>
            ) : null}
            <Link className={buttonClassName("secondary")} to="/integration/partners">
              All partners
            </Link>
          </>
        }
      />
      <Card title="Profile">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>ISA qualifier and id</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{`${p.isaQualifier}/${p.isaId}`}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Environment</dt>
            <dd className={styles.value}>{p.environment}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>997 expected within</dt>
            <dd className={styles.value}>{`${p.ackSlaMinutes} minutes`}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Created</dt>
            <dd className={styles.value}>{formatDateTime(p.createdAt)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Version</dt>
            <dd className={styles.value}>{p.version}</dd>
          </div>
        </dl>
      </Card>
      <Card title="Recent interchanges">
        {recent.error ? (
          <Alert tone="danger" title="Could not load interchanges" action={<Button size="sm" onClick={recent.reload}>Try again</Button>}>
            {recent.error.message} ({recent.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Recent interchanges"
            columns={columns}
            rows={recent.data ?? []}
            getRowKey={(row) => row.id}
            loading={recent.loading}
            onRowOpen={(row) => navigate(`/integration/interchanges/${row.id}`)}
            empty={{ title: "No interchanges yet", description: "Interchanges this partner sends, and documents sent to it, appear here." }}
          />
        )}
      </Card>
    </div>
  );
}
