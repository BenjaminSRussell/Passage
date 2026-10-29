import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { listSubjectRequests, PRIVACY_STATES, PRIVACY_TYPES, type PrivacyRequestType, type PrivacyState, type RequestFilter, type SubjectRequest } from "./api/client";
import { daysUntil, formatDateTime, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const privacyStateTone: Record<PrivacyState, BadgeTone> = {
  RECEIVED: "neutral",
  VERIFIED: "info",
  DISCOVERED: "info",
  HELD: "warning",
  APPROVED: "info",
  DELIVERED: "info",
  CLOSED: "success",
  REJECTED: "neutral",
};

const finished = (r: SubjectRequest) => r.state === "CLOSED" || r.state === "REJECTED";

/** Days left on an open request, red at 5 or fewer (PRD §9.48.6). */
export function DaysLeft({ request, now }: { request: SubjectRequest; now?: Date }) {
  if (finished(request)) return <span className={styles.muted}>{request.late ? "Closed late" : "Done"}</span>;
  const days = daysUntil(request.deadlineAt, now);
  const tone: BadgeTone = days <= 5 ? "danger" : "neutral";
  const text = days < 0 ? `${-days} d overdue` : `${days} d left`;
  return (
    <span title={formatDateTime(request.deadlineAt)}>
      <Badge tone={tone}>{text}</Badge>
    </span>
  );
}

const columns: DataTableColumn<SubjectRequest>[] = [
  { key: "subjectRef", header: "Subject", mono: true },
  { key: "type", header: "Type", render: (r) => sentence(r.type) },
  { key: "regulation", header: "Regulation", mono: true },
  { key: "state", header: "State", render: (r) => <Badge tone={privacyStateTone[r.state]}>{sentence(r.state)}</Badge> },
  { key: "receivedAt", header: "Received", render: (r) => formatDateTime(r.receivedAt) },
  { key: "deadline", header: "Deadline", align: "end", render: (r) => <DaysLeft request={r} /> },
];

/**
 * Privacy requests (PRD §9.48.6, duty PrivacyOfficer): GDPR and CCPA subject
 * requests, nearest deadline first, filtered by state and type (FR-48.10).
 */
export function PrivacyRequestsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<{ state: PrivacyState | ""; type: PrivacyRequestType | "" }>({ state: "", type: "" });
  const [filter, setFilter] = useState<RequestFilter>({});
  const [extra, setExtra] = useState<{ items: SubjectRequest[]; cursor?: string; loading: boolean; error?: string }>({ items: [], loading: false });
  const state = useLoad((signal) => listSubjectRequests(filter, signal), JSON.stringify(filter));
  const rows = [...(state.data?.items ?? []), ...extra.items];
  const cursor = extra.cursor ?? state.data?.nextCursor;

  async function more() {
    if (!cursor) return;
    setExtra((e) => ({ ...e, loading: true, error: undefined }));
    try {
      const page = await listSubjectRequests({ ...filter, cursor });
      setExtra((e) => ({ items: [...e.items, ...page.items], cursor: page.nextCursor ?? "", loading: false }));
    } catch (error) {
      setExtra((e) => ({ ...e, loading: false, error: error instanceof Error ? error.message : "Request failed" }));
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Privacy requests"
        description="GDPR and CCPA subject requests: access, portability, erasure, rectification and restriction. GDPR requests close within 30 days and CCPA requests within 45; an erasure under a legal hold waits until the hold is released."
        actions={<Link to="/compliance/privacy/requests/new">Record a request</Link>}
      />
      <form
        className={styles.toolbar}
        aria-label="Filter privacy requests"
        onSubmit={(e) => {
          e.preventDefault();
          setExtra({ items: [], loading: false });
          setFilter({ state: draft.state, type: draft.type });
        }}
      >
        <FormField id="prv-state" label="State">
          {(control) => (
            <Select {...control} value={draft.state} onChange={(e) => setDraft({ ...draft, state: e.target.value as PrivacyState | "" })}>
              <option value="">Any state</option>
              {PRIVACY_STATES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="prv-type" label="Type">
          {(control) => (
            <Select {...control} value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value as PrivacyRequestType | "" })}>
              <option value="">Any type</option>
              {PRIVACY_TYPES.map((t) => (
                <option key={t} value={t}>
                  {sentence(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load privacy requests" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Privacy requests"
          columns={columns}
          rows={rows}
          getRowKey={(r) => r.requestId}
          loading={state.loading}
          onRowOpen={(r) => navigate(`/compliance/privacy/requests/${r.requestId}`)}
          empty={{ title: "No requests match", description: "Record a subject request when one arrives from the portal, e-mail or post." }}
        />
      )}
      {extra.error ? (
        <Alert tone="danger" live="alert">
          {extra.error}
        </Alert>
      ) : null}
      {cursor && !state.loading && !state.error ? (
        <div className={styles.more}>
          <Button loading={extra.loading} onClick={() => void more()}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
