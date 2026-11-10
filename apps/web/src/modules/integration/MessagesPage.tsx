import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listMessages,
  listPartners,
  MESSAGE_STATUSES,
  type Direction,
  type MessageFilter,
  type MessageStatus,
  type MonitorMessage,
  type Problem,
} from "./api/client";
import { BulkReprocessCard } from "./BulkReprocessCard";
import { dash, formatDateTime, MESSAGE_LABELS, UUID } from "./format";
import { MessagePill } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const PAGE_SIZE = 50;

/** Pages after the first. started: a later page arrived, so its cursor (possibly none) replaces the first page's. */
interface MorePages {
  items: MonitorMessage[];
  started: boolean;
  cursor?: string;
  loading: boolean;
  error: Problem | null;
}
const noMorePages: MorePages = { items: [], started: false, loading: false, error: null };

const columns: DataTableColumn<MonitorMessage>[] = [
  { key: "transactionSet", header: "Set", mono: true },
  { key: "stControl", header: "ST02", mono: true },
  { key: "controlNumber", header: "ISA13", mono: true },
  { key: "partnerName", header: "Partner" },
  { key: "direction", header: "Direction", render: (row) => (row.direction === "IN" ? "Inbound" : "Outbound") },
  { key: "status", header: "Status", render: (row) => <MessagePill status={row.status} /> },
  { key: "error", header: "Failing rule", render: (row) => row.error || dash },
  { key: "createdAt", header: "Recorded", render: (row) => formatDateTime(row.createdAt) },
];

const asDirection = (value: string | null): Direction | "" => (value === "IN" || value === "OUT" ? value : "");
const asStatus = (value: string | null): MessageStatus | "" => (MESSAGE_STATUSES.includes(value as MessageStatus) ? (value as MessageStatus) : "");

/** Only complete values reach the service, so typing a set or control number never shows a validation error. */
const digitsOrEmpty = (value: string, min: number, max: number) => (new RegExp(`^\\d{${min},${max}}$`).test(value) ? value : "");

/** Message monitor (FR-35.10): every stored transaction, newest first, with filters, paging and bulk reprocess. */
export function MessagesPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [partnerId, setPartnerId] = useState(UUID.test(params.get("partnerId") ?? "") ? (params.get("partnerId") as string) : "");
  const [direction, setDirection] = useState<Direction | "">(asDirection(params.get("direction")));
  const [status, setStatus] = useState<MessageStatus | "">(asStatus(params.get("status")));
  const [transactionSet, setTransactionSet] = useState("");
  const [controlNumber, setControlNumber] = useState("");
  const filter: MessageFilter = {
    partnerId,
    direction,
    status,
    transactionSet: digitsOrEmpty(transactionSet, 3, 3),
    controlNumber: digitsOrEmpty(controlNumber, 1, 9),
    limit: PAGE_SIZE,
  };
  const filterKey = `messages:${filter.partnerId}:${filter.direction}:${filter.status}:${filter.transactionSet}:${filter.controlNumber}`;
  const partners = useLoad((signal) => listPartners({}, signal), "monitor-partners");
  const first = useLoad((signal) => listMessages(filter, signal), filterKey);

  // Pages after the first, appended by "Load more"; reset whenever the filter changes.
  const [more, setMore] = useState<MorePages>(noMorePages);
  useEffect(() => setMore(noMorePages), [filterKey]);
  const currentKey = useRef(filterKey);
  currentKey.current = filterKey;
  const cursor = more.started ? more.cursor : first.data?.nextCursor;
  const rows = [...(first.data?.items ?? []), ...more.items];

  async function loadMore() {
    if (!cursor) return;
    const key = filterKey;
    setMore((prev) => ({ ...prev, loading: true, error: null }));
    try {
      const page = await listMessages({ ...filter, cursor });
      // A page for a filter the user has since changed is dropped.
      if (key !== currentKey.current) return;
      setMore((prev) => ({ items: [...prev.items, ...page.items], started: true, cursor: page.nextCursor, loading: false, error: null }));
    } catch (failure) {
      if (key !== currentKey.current) return;
      setMore((prev) => ({ ...prev, loading: false, error: describeProblem(failure) }));
    }
  }

  const filtered = Boolean(partnerId || direction || status || transactionSet || controlNumber);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Message monitor"
        description="Every transaction received from or sent to a partner, newest first, with the rule it failed. Open one to see its canonical document and raw payload; reprocess failed inbound transactions from their interchange, or all of those in the list at once."
        actions={
          <Link className={buttonClassName("secondary")} to="/integration/partner-sla">
            Partner SLA
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="messages-partner" label="Partner">
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
        <FormField id="messages-direction" label="Direction">
          {(control) => (
            <Select {...control} value={direction} onChange={(event) => setDirection(asDirection(event.target.value))}>
              <option value="">Both directions</option>
              <option value="IN">Inbound</option>
              <option value="OUT">Outbound</option>
            </Select>
          )}
        </FormField>
        <FormField id="messages-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(asStatus(event.target.value))}>
              <option value="">Any status</option>
              {MESSAGE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {MESSAGE_LABELS[s].label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="messages-set" label="Transaction set" hint="Three digits, for example 850.">
          {(control) => <Input {...control} inputMode="numeric" maxLength={3} value={transactionSet} onChange={(event) => setTransactionSet(event.target.value.trim())} />}
        </FormField>
        <FormField id="messages-control" label="ISA13" hint="Interchange control number.">
          {(control) => <Input {...control} inputMode="numeric" maxLength={9} value={controlNumber} onChange={(event) => setControlNumber(event.target.value.trim())} />}
        </FormField>
      </div>
      {first.error ? (
        <Alert tone="danger" title="Could not load messages" live="alert" action={<Button size="sm" onClick={first.reload}>Try again</Button>}>
          <ProblemMessage problem={first.error} />
        </Alert>
      ) : (
        <>
          <BulkReprocessCard
            rows={rows}
            onDone={() => {
              setMore(noMorePages);
              first.reload();
            }}
          />
          <DataTable
            label="Messages"
            columns={columns}
            rows={rows}
            getRowKey={(row) => row.id}
            loading={first.loading}
            onRowOpen={(row) => navigate(`/integration/messages/${row.id}`)}
            empty={
              filtered
                ? { title: "No messages match", description: "Try another partner, direction, status, set or control number." }
                : { title: "No messages yet", description: "Transactions appear here as partners send interchanges and documents are sent to them." }
            }
          />
          {more.error ? (
            <Alert tone="danger" title="Could not load more messages" live="alert">
              <ProblemMessage problem={more.error} />
            </Alert>
          ) : null}
          {cursor && !first.loading ? (
            <div className={styles.formActions}>
              <Button onClick={loadMore} loading={more.loading}>
                Load more
              </Button>
              <span className={styles.note}>{`${rows.length} shown`}</span>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
