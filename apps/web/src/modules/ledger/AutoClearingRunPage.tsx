import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { fetchAutoClearingRule, fetchAutoClearingRules, fetchAutoClearingRun, fetchAutoClearingRuns, newKey, runAutoClearing, type AutoClearingGroup, type AutoClearingRun } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

function GroupStatus({ status }: { status: AutoClearingGroup["status"] }) {
  const tone = status === "skipped" ? "warning" : "success";
  const text = status === "proposed" ? "Would clear" : status === "cleared" ? "Cleared" : "Skipped";
  return <Badge tone={tone}>{text}</Badge>;
}

function groupColumns(query: string): DataTableColumn<AutoClearingGroup>[] {
  return [
    { key: "no", header: "No.", align: "end", mono: true },
    { key: "account", header: "Account", mono: true },
    { key: "itemCount", header: "Items", align: "end", mono: true },
    { key: "net", header: "Difference", align: "end", mono: true },
    { key: "status", header: "Result", render: (g) => <GroupStatus status={g.status} /> },
    {
      key: "detail",
      header: "Detail",
      render: (g) =>
        g.clearingDocumentId ? <Link to={`/ledger/clearings/${g.clearingDocumentId}${query}`}>Clearing document</Link> : g.reason ? `${g.reason} (${g.reasonCode})` : "",
    },
  ];
}

export function RunSummary({ run }: { run: AutoClearingRun }) {
  const simulated = run.mode === "simulate";
  return (
    <StatGroup label="Run summary">
      <StatTile label="Groups found" value={run.groupsFound} />
      <StatTile label={simulated ? "Would clear" : "Cleared"} value={run.groupsCleared} />
      <StatTile label="Skipped" value={run.groupsSkipped} tone="warning" />
      <StatTile label="Items cleared" value={run.itemsCleared} />
      <StatTile label="Items in no group" value={run.unmatchedItems} />
    </StatGroup>
  );
}

function GroupsTable({ run, query }: { run: AutoClearingRun; query: string }) {
  return (
    <>
      {run.truncated ? (
        <Alert tone="warning" title="Not every open item was read" live="status">A rule has more than 100,000 open items. The rest are picked up by the next run.</Alert>
      ) : null}
      <DataTable
        label="Groups"
        columns={groupColumns(query)}
        rows={run.groups ?? []}
        getRowKey={(g) => String(g.no)}
        empty={{ title: "No groups found", description: "No two open items share every criterion of an active rule." }}
      />
    </>
  );
}

/** Simulate and run automatic clearing (FR-11.20), with the stored runs of the ledger. */
export function AutoClearingRunPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const query = ledgerQuery(ledgerId);
  const rules = useQuery({ queryKey: ["ledger", "auto-clearing-rules", ledgerId], queryFn: () => fetchAutoClearingRules(ledgerId), enabled: ledgerId !== "" });
  const runs = useQuery({ queryKey: ["ledger", "auto-clearing-runs", ledgerId], queryFn: () => fetchAutoClearingRuns(ledgerId), enabled: ledgerId !== "" });
  const [ruleId, setRuleId] = useState("");
  const [date, setDate] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [simulation, setSimulation] = useState<AutoClearingRun | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState<"simulate" | "clear" | null>(null);
  const attempt = useRef<{ signature: string; key: string }>({ signature: "", key: "" });

  const input = () => ({ ledgerId, ...(ruleId ? { ruleId } : {}), ...(date ? { clearingDate: date } : {}) });

  async function act(mode: "simulate" | "clear", event?: FormEvent) {
    event?.preventDefault();
    setFailure(null);
    if (date !== "" && !DATE.test(date)) {
      setError("Dates use the form YYYY-MM-DD.");
      return;
    }
    setError(undefined);
    setBusy(mode);
    try {
      if (mode === "simulate") {
        setSimulation(await runAutoClearing({ ...input(), mode }));
      } else {
        // One key per user-intended run: a retry after a failure replays instead of running twice.
        const signature = JSON.stringify(input());
        if (attempt.current.signature !== signature) attempt.current = { signature, key: newKey() };
        const run = await runAutoClearing({ ...input(), mode }, attempt.current.key);
        await queryClient.invalidateQueries({ queryKey: ["ledger", "auto-clearing-runs"] });
        await queryClient.invalidateQueries({ queryKey: ["ledger", "open-items"] });
        navigate(`/ledger/auto-clearing-runs/${run.id}${query}`);
      }
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(null);
    }
  }

  const reset = () => setSimulation(null);
  const runColumns: DataTableColumn<AutoClearingRun>[] = [
    { key: "createdAt", header: "Run at", mono: true, render: (r) => r.createdAt?.replace("T", " ").replace(/\.\d+Z$|Z$/, "") },
    { key: "clearingDate", header: "Clearing date", mono: true },
    { key: "groupsCleared", header: "Cleared", align: "end", mono: true },
    { key: "groupsSkipped", header: "Skipped", align: "end", mono: true },
    { key: "itemsCleared", header: "Items", align: "end", mono: true },
    { key: "createdBy", header: "Run by" },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Automatic clearing"
        description="Groups the open items of the active rules and clears every group that nets to zero, or within its rule's tolerance. Simulate first: nothing is written until you clear."
        actions={ledgerId ? <Link to={`/ledger/auto-clearing-rules${query}`}>Rules</Link> : undefined}
      />
      <LedgerPicker id="acr-run-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Enter a ledger id to run automatic clearing on it.</p>
      ) : (
        <>
          <Card>
            <form className={styles.form} onSubmit={(e) => void act("simulate", e)} noValidate aria-label="Run automatic clearing">
              <FormField id="acr-run-rule" label="Rule" hint="Every active rule, or only one.">
                {(c) => (
                  <Select {...c} value={ruleId} onChange={(e) => { setRuleId(e.target.value); reset(); }}>
                    <option value="">All active rules</option>
                    {(rules.data ?? []).filter((r) => r.active).map((r) => (
                      <option key={r.id} value={r.id}>{r.name}</option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="acr-run-date" label="Clearing date" hint="Optional, YYYY-MM-DD. Defaults to today." error={error}>
                {(c) => <Input {...c} className={styles.mono} value={date} onChange={(e) => { setDate(e.target.value); reset(); }} invalid={Boolean(error)} />}
              </FormField>
              {failure ? (
                <div className={styles.wide}>
                  <Alert tone="danger" title="Could not run automatic clearing" live="alert">{failure}</Alert>
                </div>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" loading={busy === "simulate"}>Simulate</Button>
                {simulation && simulation.groupsCleared > 0 ? (
                  <Button variant="primary" loading={busy === "clear"} onClick={() => void act("clear")}>
                    {`Clear ${simulation.groupsCleared} ${simulation.groupsCleared === 1 ? "group" : "groups"}`}
                  </Button>
                ) : null}
              </div>
            </form>
          </Card>
          {simulation ? (
            <>
              <h2>Simulation</h2>
              <RunSummary run={simulation} />
              <GroupsTable run={simulation} query={query} />
            </>
          ) : null}
          <h2>Earlier runs</h2>
          {runs.isError ? (
            <Alert tone="danger" title="Could not load the earlier runs" live="alert" action={<Button size="sm" onClick={() => void runs.refetch()}>Try again</Button>}>
              {errorText(runs.error)}
            </Alert>
          ) : (
            <DataTable
              label="Earlier runs"
              columns={runColumns}
              rows={runs.data ?? []}
              getRowKey={(r) => r.id ?? r.createdAt ?? ""}
              loading={runs.isPending}
              empty={{ title: "No runs yet", description: "A run you commit is kept here with the reason for every skipped group." }}
              onRowOpen={(r) => navigate(`/ledger/auto-clearing-runs/${r.id}${query}`)}
            />
          )}
        </>
      )}
    </div>
  );
}

/** One stored run with the log of its groups. */
export function AutoClearingRunDetailPage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const run = useQuery({ queryKey: ["ledger", "auto-clearing-runs", "run", id], queryFn: () => fetchAutoClearingRun(id) });
  const rule = useQuery({
    queryKey: ["ledger", "auto-clearing-rules", run.data?.ledgerId, run.data?.ruleId],
    queryFn: () => fetchAutoClearingRule(run.data?.ledgerId ?? "", run.data?.ruleId ?? ""),
    enabled: Boolean(run.data?.ruleId),
  });
  const query = ledgerQuery(ledgerId || run.data?.ledgerId || "");
  return (
    <div className={styles.stack}>
      <PageHeader title="Automatic clearing run" description={run.data ? `Cleared on ${run.data.clearingDate}${run.data.createdBy ? ` by ${run.data.createdBy}` : ""}. ${run.data.ruleId ? `Rule: ${rule.data?.name ?? "loading"}.` : "All active rules."}` : undefined} actions={<Link to={`/ledger/auto-clearing${query}`}>Back to automatic clearing</Link>} />
      {run.isPending ? (
        <Skeleton height="tile" />
      ) : run.isError ? (
        <Alert tone="danger" title="Could not load the run" live="alert" action={<Button size="sm" onClick={() => void run.refetch()}>Try again</Button>}>
          {errorText(run.error)}
        </Alert>
      ) : (
        <>
          <RunSummary run={run.data} />
          <GroupsTable run={run.data} query={query} />
        </>
      )}
    </div>
  );
}
