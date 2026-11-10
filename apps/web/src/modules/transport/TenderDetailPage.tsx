import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getTender, respondTender, type Problem, type Tender, type TenderAttempt } from "./api/client";
import { Details } from "./Details";
import { dash, formatDateTime, money, stateTone, statusLabel, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const attemptColumns = (currency: string): DataTableColumn<TenderAttempt>[] => [
  { key: "rank", header: "Rank", align: "end" },
  { key: "carrierId", header: "Carrier", mono: true },
  { key: "rateQuoted", header: "Rate quoted", align: "end", render: (row) => (row.rateQuoted ? money(row.rateQuoted, currency) : dash) },
  { key: "sentAt", header: "Sent", render: (row) => formatDateTime(row.sentAt) },
  { key: "expiresAt", header: "Deadline", render: (row) => formatDateTime(row.expiresAt) },
  { key: "response", header: "Response", render: (row) => <Badge tone={stateTone(row.response)}>{statusLabel(row.response)}</Badge> },
];

/** Record the pending carrier's answer. Carriers answer through EDI 990 or the carrier portal; this is the planner's entry for a phone or e-mail answer. */
function RespondCard({ tender, onDone }: { tender: Tender; onDone(t: Tender): void }) {
  const [carrierId, setCarrierId] = useState(tender.pendingCarrierId ?? "");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<"accept" | "decline" | null>(null);

  async function answer(accept: boolean) {
    const id = carrierId.trim();
    setFailure(null);
    if (!UUID.test(id)) {
      setError("Enter the carrier id as a lower-case UUID.");
      return;
    }
    setError(undefined);
    setBusy(accept ? "accept" : "decline");
    try {
      const updated = await respondTender(tender.id, { carrierId: id, accept });
      setCarrierId(updated.pendingCarrierId ?? "");
      onDone(updated);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card title="Carrier response" headingLevel={3}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {tender.overdue
            ? "The deadline has passed. A response now is rejected and the load moves on to the next carrier."
            : `Waiting on rank ${tender.attempts.length} until ${formatDateTime(tender.pendingExpiresAt)}.`}
        </p>
        <div className={styles.inlineForm}>
          <FormField id="respond-carrier" label="Responding carrier id" error={error}>
            {(control) => <Input {...control} className={styles.mono} value={carrierId} onChange={(e) => setCarrierId(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <Button variant="primary" loading={busy === "accept"} onClick={() => answer(true)}>
            Record acceptance
          </Button>
          <Button loading={busy === "decline"} onClick={() => answer(false)}>
            Record decline
          </Button>
        </div>
        {failure ? (
          <Alert tone="danger" title="The response was not accepted" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
      </div>
    </Card>
  );
}

/** One tender's cascade: who was asked, when, and what they answered (BR-34.3). */
export function TenderDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getTender(id, signal), `tender:${id}`);
  const [updated, setUpdated] = useState<Tender | null>(null);
  const tender = updated && updated.id === state.data?.id ? updated : state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Tender" />
        <Alert tone="danger" title="Could not load the tender" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link to="/transport/tenders">Back to tenders</Link>
      </div>
    );
  }
  if (!tender) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Tender" />
        <Card>
          <p className={styles.note}>Loading the tender…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const currency = tender.currency ?? "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Tender ${tender.id.slice(0, 8)}`}
        meta={
          <span className={styles.flags}>
            <Badge tone={stateTone(tender.status)}>{statusLabel(tender.status)}</Badge>
            {tender.overdue ? <Badge tone="warning">Overdue</Badge> : null}
          </span>
        }
      />
      {tender.status === "TENDER_FAILED" ? (
        <Alert tone="danger" title="All carriers declined or timed out (TMS-0106)">
          A planner task is open for this load. Get a spot quote or update the route guide, then start a new tender.
        </Alert>
      ) : null}
      <Card>
        <Details
          entries={[
            { term: "Tender", value: tender.id, mono: true },
            { term: "Load", value: tender.loadId, mono: true },
            { term: "Accepted by", value: tender.acceptedCarrierId ?? dash, mono: true },
            { term: "Response time", value: `${tender.timeoutMin} min per carrier` },
            { term: "Started", value: formatDateTime(tender.createdAt) },
            { term: "Carriers", value: `${tender.attempts.length} of ${tender.carriers.length} asked` },
          ]}
        />
      </Card>
      {tender.status === "PENDING" ? <RespondCard key={tender.version} tender={tender} onDone={setUpdated} /> : null}
      <Card title="Cascade" headingLevel={3}>
        <DataTable label="Tender attempts" columns={attemptColumns(currency)} rows={tender.attempts} getRowKey={(row) => String(row.rank)} />
      </Card>
      <div className={styles.actions}>
        <Link to={`/transport/tracking/${tender.loadId}`}>Load tracking</Link>
        <Link to="/transport/tenders">Back to tenders</Link>
      </div>
    </div>
  );
}
