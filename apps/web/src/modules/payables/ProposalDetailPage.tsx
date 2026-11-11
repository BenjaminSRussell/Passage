import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { EntityName } from "../../shell/lookup/EntityName";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, ConfirmDialog, DataTable, PageHeader, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { approveProposal, cancelProposal, getOpenItem, getProposal, startRun, type Proposal, type ProposalGroup, type ProposalLine } from "./api/client";
import { EXCLUDED_REASON, formatAmount, METHOD_LABELS, PROPOSAL_LABELS } from "./format";

function InvoiceNumber({ id }: { id: string }) {
  const item = useQuery({ queryKey: ["payables", "open-item", id], queryFn: () => getOpenItem(id), staleTime: Infinity });
  return <>{item.data ? item.data.invoiceNumber : item.isError ? "Not available" : "Loading…"}</>;
}

const LINE_PAGE = 10;

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

function counts(proposal: Proposal) {
  return { payments: proposal.groups.length, invoices: proposal.groups.reduce((n, g) => n + g.lines.length, 0) };
}

export function ProposalDetailPage() {
  const { id = "" } = useParams();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { legalEntityId } = useWorkContext();
  const duties = usePrincipalDuties();
  const canReadItems = holdsDuty(duties, "open-item.read");
  const [confirm, setConfirm] = useState<"approve" | "cancel" | null>(null);
  const [shown, setShown] = useState<string | null>(null);
  const [visibleLines, setVisibleLines] = useState(LINE_PAGE);
  const [notice, setNotice] = useState("");

  const proposal = useQuery({ queryKey: ["payables", "proposal", id], queryFn: () => getProposal(id) });

  const decide = useMutation({
    mutationFn: (action: "approve" | "cancel") => (action === "approve" ? approveProposal(id) : cancelProposal(id)),
    onSuccess: (updated, action) => {
      setNotice(action === "approve" ? "The proposal is approved. A different person can now start the payment run." : "The proposal is cancelled and its invoices can be proposed again.");
      queryClient.setQueryData(["payables", "proposal", id], updated);
      void queryClient.invalidateQueries({ queryKey: ["payables", "proposals"] });
    },
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: ["payables", "proposal", id] });
    },
  });
  const start = useMutation({
    mutationFn: () => startRun(legalEntityId, id),
    onSuccess: (run) => {
      void queryClient.invalidateQueries({ queryKey: ["payables", "proposal", id] });
      navigate(`/payables/runs/${run.id}`);
    },
  });
  const busy = decide.isPending || start.isPending;

  function submitDecision() {
    if (confirm === null || busy) return;
    const action = confirm;
    setConfirm(null);
    decide.mutate(action);
  }

  if (proposal.isPending) return <p role="status">Loading the payment proposal…</p>;
  if (proposal.isError) {
    return (
      <div className={styles.stack}>
        <Alert tone="danger" title="Could not load the payment proposal" live="alert" action={<Button size="sm" onClick={() => void proposal.refetch()}>Try again</Button>}>
          <ProblemMessage problem={proposal.error} />
        </Alert>
      </div>
    );
  }

  const p = proposal.data;
  const draft = p.status === "DRAFT";
  const canApprove = draft && holdsDuty(duties, "payment-proposal.approve");
  const canCancel = draft && holdsDuty(duties, "payment-proposal.write");
  // A proposal names its run once one starts, so a running, paid or failed proposal links to it. Without the id
  // (a run that is not started yet) an executor starts it; starting again answers the run that already exists.
  const runLink = p.paymentRunId && holdsDuty(duties, "payment-run.read") ? `/payables/runs/${p.paymentRunId}` : null;
  const canRun = !runLink && (p.status === "APPROVED" || p.status === "EXECUTING") && holdsDuty(duties, "payment-run.execute");
  const { payments, invoices } = counts(p);
  const withheld = Object.entries(p.totalWithheld ?? {});
  const open = p.groups.find((g) => g.id === shown);

  const groupColumns: DataTableColumn<ProposalGroup>[] = [
    { key: "vendor", header: "Vendor", render: (g) => <EntityName entity="vendor" id={g.vendorId} /> },
    { key: "paymentMethod", header: "Method", render: (g) => METHOD_LABELS[g.paymentMethod] },
    { key: "gross", header: "Invoices", align: "end", mono: true, render: (g) => formatAmount(g.gross, g.currency) },
    { key: "discount", header: "Discount", align: "end", mono: true, render: (g) => formatAmount(g.discount, g.currency) },
    { key: "withheld", header: "Tax withheld", align: "end", mono: true, render: (g) => formatAmount(g.withheld ?? "0", g.currency) },
    { key: "net", header: "To pay", align: "end", mono: true, render: (g) => formatAmount(g.net, g.currency) },
    { key: "valued", header: `In ${p.legalEntityCurrency}`, align: "end", mono: true, render: (g) => formatAmount(g.valued, p.legalEntityCurrency) },
    {
      key: "lines",
      header: "Invoices in payment",
      render: (g) => (
        <Button size="sm" onClick={() => {
            setVisibleLines(LINE_PAGE);
            setShown(shown === g.id ? null : g.id);
          }}>
          {shown === g.id ? "Hide invoices" : `Show ${plural(g.lines.length, "invoice", "invoices")}`}
        </Button>
      ),
    },
  ];

  const lineColumns = (currency: string): DataTableColumn<ProposalLine>[] => [
    ...(canReadItems ? [{ key: "invoice", header: "Document", mono: true, render: (l: ProposalLine) => <InvoiceNumber id={l.openItemId} /> }] : []),
    { key: "openAmount", header: "Amount open", align: "end", mono: true, render: (l) => formatAmount(l.openAmount, currency) },
    { key: "discount", header: "Discount", align: "end", mono: true, render: (l) => formatAmount(l.discount, currency) },
    { key: "withheld", header: "Tax withheld", align: "end", mono: true, render: (l) => formatAmount(l.withheld ?? "0", currency) },
    { key: "pay", header: "To pay", align: "end", mono: true, render: (l) => formatAmount(l.pay, currency) },
  ];

  const confirmCopy =
    confirm === "approve"
      ? {
          title: "Approve this payment proposal?",
          description: `${plural(payments, "payment", "payments")} for ${plural(invoices, "invoice", "invoices")}, ${formatAmount(p.totalValued, p.legalEntityCurrency)} in total. You can't change it after approval.`,
          confirmLabel: "Approve proposal",
        }
      : {
          title: "Cancel this payment proposal?",
          description: `${plural(invoices, "invoice", "invoices")} go back to the open items and can be put on a new proposal.`,
          confirmLabel: "Cancel proposal",
        };

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Payment proposal for ${p.runDate}`}
        description={`Created on ${p.createdAt.slice(0, 10)}${p.approvedBy ? " and approved" : ""}. Invoices due by ${p.dueBy}.`}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/payables">
              All proposals
            </Link>
            {canCancel ? <Button disabled={busy} onClick={() => { decide.reset(); setConfirm("cancel"); }}>Cancel proposal</Button> : null}
            {runLink ? (
              <Link className={buttonClassName(p.status === "EXECUTING" ? "primary" : "secondary")} to={runLink}>
                Open payment run
              </Link>
            ) : null}
            {canRun ? (
              <Button variant="primary" disabled={busy} loading={start.isPending} onClick={() => start.mutate()}>
                {p.status === "EXECUTING" ? "Open payment run" : "Start payment run"}
              </Button>
            ) : null}
            {canApprove ? <Button variant="primary" disabled={busy} onClick={() => { decide.reset(); setConfirm("approve"); }}>Approve proposal</Button> : null}
          </>
        }
      />
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {decide.isError ? (
        <Alert tone="danger" title={`Could not ${decide.variables === "cancel" ? "cancel" : "approve"} the proposal`} live="alert">
          <ProblemMessage problem={decide.error} />
        </Alert>
      ) : null}
      {start.isError ? (
        <Alert tone="danger" title="Could not start the payment run" live="alert">
          <ProblemMessage problem={start.error} />
        </Alert>
      ) : null}
      {draft && !canApprove ? <p className={styles.note}>You don't have approval rights for payment proposals. Ask an approver to approve this one.</p> : null}
      {busy ? <p role="status">{decide.variables === "cancel" ? "Cancelling the proposal…" : "Approving the proposal…"}</p> : null}
      <StatGroup label="Proposal totals">
        <StatTile label="Status" value={<StatusPill status={PROPOSAL_LABELS[p.status]} />} />
        <StatTile label="Payments" value={payments} />
        <StatTile label="Invoices" value={invoices} />
        <StatTile label={`Total in ${p.legalEntityCurrency}`} value={formatAmount(p.totalValued, p.legalEntityCurrency)} />
        {withheld.map(([currency, amount]) => (
          <StatTile key={currency} label={`Tax withheld in ${currency}`} value={formatAmount(amount, currency)} />
        ))}
      </StatGroup>
      {(p.excludedItems ?? []).length > 0 ? (
        <Alert tone="warning" title="Left out of this proposal">
          <ul>
            {(p.excludedItems ?? []).map((x) => (
              <li key={x.itemId}>
                <EntityName entity="vendor" id={x.vendorId} />{canReadItems ? <>, invoice <InvoiceNumber id={x.itemId} /></> : null}. {EXCLUDED_REASON}
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <DataTable
        label="Payments in this proposal"
        columns={groupColumns}
        rows={p.groups}
        getRowKey={(g) => g.id}
        empty={{ title: "No payments", description: "This proposal has no invoices." }}
      />
      {open ? (
        <Card>
          <h2 className={styles.legend}>
            Invoices paid to <EntityName entity="vendor" id={open.vendorId} /> in {open.currency}
          </h2>
          <DataTable label="Invoices in the payment" columns={lineColumns(open.currency)} rows={open.lines.slice(0, visibleLines)} getRowKey={(l) => l.openItemId} />
          {open.lines.length > visibleLines ? (
            <Button onClick={() => setVisibleLines((n) => n + LINE_PAGE)}>{`Show more invoices (${open.lines.length - visibleLines} left)`}</Button>
          ) : null}
        </Card>
      ) : null}
      {confirm ? (
        <ConfirmDialog {...confirmCopy} cancelLabel="Go back" onConfirm={submitDecision} onCancel={() => setConfirm(null)} />
      ) : null}
    </div>
  );
}
