import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { EntityName } from "../../shell/lookup/EntityName";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, PageHeader, StatGroup, StatTile, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { downloadRunFile, failRun, getRun, runStep, type PaymentRun, type RunPayment, type RunStep } from "./api/client";
import { formatAmount, METHOD_LABELS, personLabel, RUN_LABELS } from "./format";

interface StepInfo {
  step: RunStep;
  label: string;
  doing: string;
  done: (r: PaymentRun) => boolean;
  explain: string;
}

const STEPS: StepInfo[] = [
  { step: "generate-file", label: "Generate the bank file", doing: "Generating the bank file…", done: (r) => !!r.fileGeneratedAt, explain: "Builds the payment file your bank imports." },
  { step: "post-journal", label: "Post to the ledger", doing: "Posting to the ledger…", done: (r) => !!r.journalPostedAt, explain: "Records the payments in the general ledger." },
  { step: "settle", label: "Settle the invoices", doing: "Settling the invoices…", done: (r) => !!r.settledAt, explain: "Marks the vendor invoices as paid." },
  { step: "complete", label: "Complete the run", doing: "Completing the run…", done: (r) => r.status === "COMPLETED", explain: "Closes the run once the bank has the file." },
];

export function nextStep(run: PaymentRun): StepInfo | undefined {
  if (run.status !== "RUNNING") return undefined;
  return STEPS.find((s) => !s.done(run));
}

export function RunPage() {
  const { id = "" } = useParams();
  const queryClient = useQueryClient();
  const duties = usePrincipalDuties();
  const { identity } = useWorkContext();
  const canExecute = holdsDuty(duties, "payment-run.execute");
  const [failing, setFailing] = useState(false);
  const [reason, setReason] = useState("");
  const [saved, setSaved] = useState("");

  const run = useQuery({ queryKey: ["payables", "run", id], queryFn: () => getRun(id) });

  const step = useMutation({
    mutationFn: (s: StepInfo) => runStep(id, s.step),
    onSuccess: (updated) => {
      queryClient.setQueryData(["payables", "run", id], updated);
      void queryClient.invalidateQueries({ queryKey: ["payables", "proposal"] });
      void queryClient.invalidateQueries({ queryKey: ["payables", "proposals"] });
    },
    onError: () => void queryClient.invalidateQueries({ queryKey: ["payables", "run", id] }),
  });
  const fail = useMutation({
    mutationFn: (text: string) => failRun(id, text),
    onSuccess: (updated) => {
      setFailing(false);
      setReason("");
      queryClient.setQueryData(["payables", "run", id], updated);
      void queryClient.invalidateQueries({ queryKey: ["payables", "proposal"] });
    },
  });
  const download = useMutation({
    mutationFn: async () => {
      const blob = await downloadRunFile(id);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `payment-run-${run.data?.fileNumber ?? id}.xml`;
      link.click();
      URL.revokeObjectURL(url);
    },
    onSuccess: () => setSaved("The bank file was downloaded."),
  });
  const busy = step.isPending || fail.isPending;

  if (run.isPending) return <p role="status">Loading the payment run…</p>;
  if (run.isError) {
    return (
      <Alert tone="danger" title="Could not load the payment run" live="alert" action={<Button size="sm" onClick={() => void run.refetch()}>Try again</Button>}>
        <ProblemMessage problem={run.error} />
      </Alert>
    );
  }

  const r = run.data;
  const next = nextStep(r);
  const total = r.payments.reduce((n, p) => n + Number(p.valuedAmount), 0);

  const columns: DataTableColumn<RunPayment>[] = [
    { key: "vendor", header: "Vendor", render: (p) => <EntityName entity="vendor" id={p.vendorId} /> },
    { key: "method", header: "Method", render: (p) => METHOD_LABELS[p.paymentMethod] },
    { key: "ref", header: "Payment reference", mono: true, render: (p) => p.endToEndId },
    { key: "amount", header: "Amount", align: "end", mono: true, render: (p) => formatAmount(p.amount, p.currency) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Payment run"
        description={`Started by ${personLabel(r.requestedBy, identity)} on ${r.createdAt.slice(0, 10)}.`}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/payables/proposals/${r.proposalId}`}>
              Payment proposal
            </Link>
            {r.fileGeneratedAt && canExecute ? (
              <Button loading={download.isPending} onClick={() => download.mutate()}>
                Download bank file
              </Button>
            ) : null}
          </>
        }
      />
      {saved ? <Alert tone="success" live="status">{saved}</Alert> : null}
      {download.isError ? (
        <Alert tone="danger" title="Could not download the bank file" live="alert">
          <ProblemMessage problem={download.error} />
        </Alert>
      ) : null}
      {step.isError ? (
        <Alert tone="danger" title={`Could not ${step.variables?.label.toLowerCase() ?? "do that step"}`} live="alert">
          <ProblemMessage problem={step.error} />
        </Alert>
      ) : null}
      {r.status === "FAILED" ? (
        <Alert tone="danger" title="This payment run failed">
          {r.failureReason ?? "No reason was recorded."} Create a new payment proposal to pay these invoices.
        </Alert>
      ) : null}
      {r.status === "COMPLETED" ? <Alert tone="success" title="This payment run is complete">The vendor invoices are paid.</Alert> : null}
      <StatGroup label="Run totals">
        <StatTile label="Status" value={<StatusPill status={RUN_LABELS[r.status]} />} />
        <StatTile label="Payments" value={r.paymentCount ?? r.payments.length} />
        <StatTile label="Total (legal entity currency)" value={total.toFixed(2)} />
        {r.fileNumber ? <StatTile label="Bank file number" value={r.fileNumber} /> : null}
      </StatGroup>
      <Card>
        <h2 className={styles.legend}>Steps</h2>
        <ol aria-label="Run steps">
          {STEPS.map((s) => {
            const complete = s.done(r);
            const current = next?.step === s.step;
            return (
              <li key={s.step}>
                <strong>{s.label}</strong> {complete ? "(done)" : current ? "(next)" : ""}
                <div className={styles.note}>{s.explain}</div>
              </li>
            );
          })}
        </ol>
        {next && canExecute ? (
          <Button variant="primary" disabled={busy} loading={step.isPending} onClick={() => step.mutate(next)}>
            {next.label}
          </Button>
        ) : null}
        {next && !canExecute ? <p className={styles.note}>You don't have rights to run payments. Ask a payment run executor to continue.</p> : null}
        {step.isPending ? <p role="status">{step.variables?.doing}</p> : null}
      </Card>
      {r.status === "RUNNING" && canExecute ? (
        failing ? (
          <Card>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                if (reason.trim() !== "" && !busy) fail.mutate(reason.trim());
              }}
            >
              <FormField id="run-fail-reason" label="Why is this run failing?" hint="Recorded on the run.">
                {(c) => <Textarea {...c} required maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} />}
              </FormField>
              {fail.isError ? (
                <Alert tone="danger" title="Could not mark the run as failed" live="alert">
                  <ProblemMessage problem={fail.error} />
                </Alert>
              ) : null}
              <Button type="submit" variant="primary" disabled={busy || reason.trim() === ""}>
                Mark run as failed
              </Button>
              <Button onClick={() => setFailing(false)}>Go back</Button>
            </form>
          </Card>
        ) : (
          <div>
            <Button disabled={busy} onClick={() => setFailing(true)}>
              Mark run as failed
            </Button>
          </div>
        )
      ) : null}
      <DataTable label="Payments in this run" columns={columns} rows={r.payments} getRowKey={(p) => p.endToEndId} empty={{ title: "No payments", description: "This run has no payments." }} />
    </div>
  );
}
