import { useRef, useState } from "react";
import { Alert, Badge, Button, Card, FormField, Input, ListPage, type BadgeTone, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { ProblemMessage } from "../../shell/errors";
import {
  describeProblem,
  DISPUTE_STATUSES,
  listPartners,
  listSelfBillDisputes,
  resolveSelfBillDispute,
  runSelfBillSweep,
  type DisputeStatus,
  type Problem,
  type SelfBillDispute,
} from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const DISPUTE_STATUS_LABELS: Record<DisputeStatus, { label: string; tone: BadgeTone }> = {
  OPEN: { label: "Open", tone: "warning" },
  ACCEPTED: { label: "Their figures accepted", tone: "success" },
  REJECTED: { label: "Our figures kept", tone: "neutral" },
};

export const DISPUTE_KIND_LABELS: Record<string, string> = {
  DIFFERENCE: "Differs from what shipped",
  UNMATCHED: "No matching shipment",
  UNBILLED: "Shipment never self-billed",
};

/** The checks of the decision form: a note is kept with the decision and is at most 500 characters. */
export function validateDecision(note: string): Record<string, string> {
  return note.length > 500 ? { note: "Use at most 500 characters." } : {};
}

function useDisputeRows(reloadKey: number) {
  return function useRows(query: ListQuery): ListData<SelfBillDispute> {
    const status = (query.filters.status ?? "") as DisputeStatus | "";
    const partnerId = query.filters.partnerId ?? "";
    const state = useLoad((signal) => listSelfBillDisputes({ status, partnerId }, signal), `disputes|${status}|${partnerId}|${reloadKey}`);
    return {
      rows: state.data ?? [],
      loading: state.loading,
      loadingMore: false,
      error: state.error ?? undefined,
      forbidden: state.error?.code === "AUTH-0003",
      hasMore: false,
      loadMore: () => undefined,
      retry: state.reload,
    };
  };
}

/** The worklist of self-billing disputes (35-5): what a customer's self-billed document got wrong, and the shipments it never billed. */
export function SelfBillingDisputesPage() {
  const partners = useLoad((signal) => listPartners({}, signal), "dispute-partners");
  const [reloadKey, setReloadKey] = useState(0);
  const [selected, setSelected] = useState<SelfBillDispute | null>(null);
  const names = new Map((partners.data ?? []).map((p) => [p.id, p.name]));

  const columns: ListColumn<SelfBillDispute>[] = [
    { key: "partnerId", header: "Customer", sortValue: (row) => names.get(row.partnerId) ?? "", render: (row) => names.get(row.partnerId) ?? "—", csv: (row) => names.get(row.partnerId) ?? "" },
    { key: "kind", header: "Problem", render: (row) => DISPUTE_KIND_LABELS[row.kind] ?? row.kind, sortValue: (row) => DISPUTE_KIND_LABELS[row.kind] ?? row.kind, csv: (row) => DISPUTE_KIND_LABELS[row.kind] ?? row.kind },
    { key: "detail", header: "What differs", sortValue: (row) => row.detail, csv: (row) => row.detail },
    {
      key: "status",
      header: "Status",
      render: (row) => {
        const s = DISPUTE_STATUS_LABELS[row.status] ?? { label: row.status, tone: "neutral" as const };
        return <Badge tone={s.tone}>{s.label}</Badge>;
      },
      sortValue: (row) => DISPUTE_STATUS_LABELS[row.status]?.label ?? row.status,
      csv: (row) => DISPUTE_STATUS_LABELS[row.status]?.label ?? row.status,
    },
    { key: "createdAt", header: "Opened", render: (row) => formatDateTime(row.createdAt), sortValue: (row) => row.createdAt, csv: (row) => row.createdAt },
  ];
  const filters: ListFilterDef[] = [
    { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: DISPUTE_STATUSES.map((s) => ({ value: s, label: DISPUTE_STATUS_LABELS[s].label })) },
    { kind: "select", key: "partnerId", label: "Customer", anyLabel: "All customers", options: (partners.data ?? []).map((p) => ({ value: p.id, label: p.name })) },
  ];

  return (
    <div className={styles.stack}>
      <SweepCard onDone={() => setReloadKey((n) => n + 1)} />
      {selected ? (
        <DecisionCard
          dispute={selected}
          customer={names.get(selected.partnerId) ?? "the customer"}
          onClose={() => setSelected(null)}
          onDone={() => {
            setSelected(null);
            setReloadKey((n) => n + 1);
          }}
        />
      ) : null}
      <ListPage
        id="integration-self-billing-disputes"
        title="Self-billing disputes"
        description="Where a customer's self-billed invoice differs from what you shipped, has a line with no shipment, or never came for a shipment. Open a row to accept the customer's figures or keep yours."
        noun="disputes"
        singular="dispute"
        columns={columns}
        filters={filters}
        useRows={useDisputeRows(reloadKey)}
        getRowKey={(row) => row.id}
        onRowOpen={(row) => setSelected(row)}
        empty={{ title: "No disputes", description: "Differences and unbilled shipments are opened here when a self-billed document is matched." }}
      />
    </div>
  );
}

function SweepCard({ onDone }: { onDone(): void }) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  async function run() {
    setBusy(true);
    setFailure(null);
    try {
      const out = await runSelfBillSweep();
      setResult(`${out.reconciled} document${out.reconciled === 1 ? "" : "s"} matched, ${out.unbilled.length} unbilled shipment${out.unbilled.length === 1 ? "" : "s"} disputed.`);
      onDone();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card
      title="Sweep"
      actions={
        <Button size="sm" onClick={() => void run()} loading={busy}>
          Run the sweep now
        </Button>
      }
    >
      <p className={styles.note}>The sweep runs on a schedule: it matches self-billed documents that have not been matched and opens a dispute for every shipment still unbilled after the customer's wait. Run it now to see the result straight away.</p>
      {result ? (
        <Alert tone="info" title="Sweep finished" live="status">
          {result}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The sweep failed" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
    </Card>
  );
}

function DecisionCard({ dispute, customer, onClose, onDone }: { dispute: SelfBillDispute; customer: string; onClose(): void; onDone(): void }) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<DisputeStatus | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const inFlight = useRef(false);
  const open = dispute.status === "OPEN";

  async function decide(decision: "ACCEPTED" | "REJECTED") {
    if (inFlight.current) return;
    const found = validateDecision(note);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    inFlight.current = true;
    setBusy(decision);
    setFailure(null);
    try {
      await resolveSelfBillDispute(dispute.id, decision, note.trim());
      onDone();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      inFlight.current = false;
      setBusy(null);
    }
  }

  return (
    <Card
      title={`${DISPUTE_KIND_LABELS[dispute.kind] ?? dispute.kind} · ${customer}`}
      actions={
        <Button size="sm" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className={styles.stack}>
        <p>{dispute.detail}</p>
        {!open ? (
          <Alert tone="info" title={DISPUTE_STATUS_LABELS[dispute.status]?.label ?? dispute.status}>
            {dispute.resolution ? dispute.resolution : "Decided."}
            {dispute.resolvedAt ? ` ${formatDateTime(dispute.resolvedAt)}.` : ""}
          </Alert>
        ) : (
          <>
            <FormField id="dispute-note" label="Note" error={errors.note} hint="Kept with the decision.">
              {(control) => <Input {...control} value={note} maxLength={500} onChange={(event) => setNote(event.target.value)} />}
            </FormField>
            {failure ? (
              <Alert tone="danger" title="The decision was not saved" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            ) : null}
            <div className={styles.formActions}>
              <Button variant="primary" onClick={() => void decide("ACCEPTED")} loading={busy === "ACCEPTED"} disabled={busy !== null}>
                Accept their figures
              </Button>
              <Button onClick={() => void decide("REJECTED")} loading={busy === "REJECTED"} disabled={busy !== null}>
                Keep our figures
              </Button>
            </div>
          </>
        )}
      </div>
    </Card>
  );
}
