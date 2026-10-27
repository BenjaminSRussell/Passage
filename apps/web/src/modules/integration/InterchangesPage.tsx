import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { ACK_STATUSES, listInterchanges, listPartners, type AckStatus, type Direction, type InterchangeSummary } from "./api/client";
import { ACK_LABELS, formatDateTime } from "./format";
import { AckPill } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<InterchangeSummary>[] = [
  { key: "controlNumber", header: "ISA13", mono: true },
  { key: "partnerName", header: "Partner" },
  { key: "direction", header: "Direction", render: (row) => (row.direction === "IN" ? "Inbound" : "Outbound") },
  { key: "standard", header: "Standard", render: (row) => `${row.standard} ${row.version}` },
  { key: "messageCount", header: "Transactions", align: "end", render: (row) => String(row.messageCount) },
  { key: "failedCount", header: "Failed", align: "end", render: (row) => String(row.failedCount) },
  { key: "ackStatus", header: "997", render: (row) => <AckPill status={row.ackStatus} /> },
  { key: "receivedAt", header: "Recorded", render: (row) => formatDateTime(row.receivedAt) },
];

const asDirection = (value: string | null): Direction | "" => (value === "IN" || value === "OUT" ? value : "");

/** Interchanges received from and sent to partners, newest first (FR-35.10, list only). */
export function InterchangesPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [partnerId, setPartnerId] = useState("");
  const [ackStatus, setAckStatus] = useState<AckStatus | "">("");
  const [direction, setDirection] = useState<Direction | "">(asDirection(params.get("direction")));
  const partners = useLoad((signal) => listPartners({}, signal), "interchange-partners");
  const state = useLoad(
    (signal) => listInterchanges({ partnerId, ackStatus, direction, limit: 100 }, signal),
    `interchanges:${partnerId}:${ackStatus}:${direction}`,
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Interchanges"
        description="Every X12 interchange received or sent. For an inbound one the 997 column is the acknowledgment we sent back; for an outbound one it is the partner's 997, pending until it arrives."
        actions={
          <>
            <Link className={buttonClassName("primary")} to="/integration/interchanges/outbound">
              Send document
            </Link>
            <Link className={buttonClassName("secondary")} to="/integration/interchanges/receive">
              Receive interchange
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="interchanges-partner" label="Partner">
          {(control) => (
            <Select {...control} value={partnerId} onChange={(event) => setPartnerId(event.target.value)}>
              <option value="">All partners</option>
              {(partners.data ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {`${p.name} (${p.environment})`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="interchanges-direction" label="Direction">
          {(control) => (
            <Select {...control} value={direction} onChange={(event) => setDirection(asDirection(event.target.value))}>
              <option value="">Both directions</option>
              <option value="IN">Inbound</option>
              <option value="OUT">Outbound</option>
            </Select>
          )}
        </FormField>
        <FormField id="interchanges-status" label="997 status">
          {(control) => (
            <Select {...control} value={ackStatus} onChange={(event) => setAckStatus(event.target.value as AckStatus | "")}>
              <option value="">Any status</option>
              {ACK_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {ACK_LABELS[s].label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load interchanges" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Interchanges"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/integration/interchanges/${row.id}`)}
          empty={
            partnerId || ackStatus || direction
              ? { title: "No interchanges match", description: "Try another partner, direction or status." }
              : { title: "No interchanges yet", description: "Interchanges appear here as partners send them, or when you receive one by hand." }
          }
        />
      )}
    </div>
  );
}
