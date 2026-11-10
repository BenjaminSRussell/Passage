import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { actOnRealignment, fetchRealignment, newKey, type RealignmentRow, type RealignmentRun } from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";
import { describeMapping, RealignmentStatusBadge } from "./RealignmentsPage";

const columns: DataTableColumn<RealignmentRow>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "account", header: "Account", mono: true },
  { key: "oldCombination", header: "From", mono: true },
  { key: "newCombination", header: "To", mono: true },
  { key: "lineCount", header: "Lines", align: "end", mono: true },
  { key: "net", header: "Net moved", align: "end", mono: true },
  {
    key: "quantities",
    header: "Quantities moved",
    align: "end",
    mono: true,
    render: (r) => (r.quantities ?? []).map((q) => `${q.net} ${q.unit}`).join(", "),
  },
];

function Actions({ run, ledgerId }: { run: RealignmentRun; ledgerId: string }) {
  const queryClient = useQueryClient();
  const [reason, setReason] = useState(run.reasonCode ?? "");
  const [comment, setComment] = useState("");
  const [postingDate, setPostingDate] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [keys] = useState(() => ({ post: newKey(), reverse: newKey(), cancel: newKey() }));
  if (run.status === "reversed" || run.status === "cancelled") return null;

  async function act(action: "post" | "reverse" | "cancel") {
    setFailure(null);
    if (action !== "cancel" && reason.trim() === "") {
      setError("Enter the reason code of the realignment.");
      return;
    }
    setError(undefined);
    setBusy(action);
    try {
      const body =
        action === "cancel"
          ? {}
          : { reasonCode: reason.trim(), reasonComment: comment.trim() || undefined, postingDate: action === "reverse" && postingDate ? postingDate : undefined };
      await actOnRealignment(action, ledgerId, run.id, body, keys[action]);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "realignments"] });
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(null);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    void act(run.status === "proposed" ? "post" : "reverse");
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={run.status === "proposed" ? "Post realignment" : "Reverse realignment"}>
        <FormField id="ra-reason" label="Reason code" hint="A code from the shared reason code catalogue." error={error}>
          {(c) => <Input {...c} className={styles.mono} value={reason} onChange={(e) => setReason(e.target.value.toUpperCase())} invalid={Boolean(error)} />}
        </FormField>
        <FormField id="ra-comment" label="Comment" optional>
          {(c) => <Input {...c} maxLength={255} value={comment} onChange={(e) => setComment(e.target.value)} />}
        </FormField>
        {run.status === "posted" ? (
          <FormField id="ra-date" label="Reversal date" optional hint="Leave empty to reverse each entry on its own date. Set a date in an open period when a period of the run has been closed since.">
            {(c) => <Input {...c} type="date" value={postingDate} onChange={(e) => setPostingDate(e.target.value)} />}
          </FormField>
        ) : null}
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="The realignment was not changed" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          {run.status === "proposed" ? (
            <>
              <Button type="submit" variant="primary" loading={busy === "post"}>Post realignment</Button>
              <Button onClick={() => void act("cancel")} loading={busy === "cancel"}>Cancel run</Button>
            </>
          ) : (
            <Button type="submit" variant="danger" loading={busy === "reverse"}>Reverse realignment</Button>
          )}
        </div>
      </form>
    </Card>
  );
}

/** One realignment run: simulation rows, the entries it posted and the actions its status allows. */
export function RealignmentPage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "realignments", ledgerId, id], queryFn: () => fetchRealignment(ledgerId, id), enabled: ledgerId !== "" });
  const run = result.data;
  return (
    <div className={styles.stack}>
      <PageHeader title="Dimension realignment" description={run ? describeMapping(run) : undefined} actions={run ? <RealignmentStatusBadge status={run.status} /> : undefined} />
      {ledgerId === "" ? (
        <p className={styles.note}>Open this page from the realignment list of a ledger so it knows which ledger to use.</p>
      ) : result.isPending ? (
        <Skeleton height="tile" />
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the realignment run" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <>
          <StatGroup label="Run summary">
            <StatTile label="Balances moved" value={run!.combinationsAffected} />
            <StatTile label="Lines behind them" value={run!.lineCount} />
            <StatTile label={`Amount moved (${run!.currency})`} value={run!.amountMoved} />
            <StatTile label="Entries posted" value={run!.entryIds.length} />
          </StatGroup>
          {run!.reasonCode ? <p className={styles.note}>Reason {run!.reasonCode}{run!.postedBy ? `, posted by ${run!.postedBy}` : ""}.</p> : null}
          {run!.previewTruncated ? <Alert tone="info" title="Only the first rows are listed" live="status">The run moves more balances than are shown; posting moves all of them.</Alert> : null}
          <DataTable
            label="Affected balances"
            columns={columns}
            rows={run!.preview}
            getRowKey={(r) => `${r.period}|${r.account}|${r.oldCombination}`}
            empty={{ title: "Nothing to move", description: "No balance holds one of the old values in these accounts and periods." }}
          />
          {run!.entryIds.length > 0 ? (
            <p className={styles.note}>
              Reclassification entries:{" "}
              {run!.entryIds.map((e, i) => (
                <span key={e}>
                  {i > 0 ? ", " : ""}
                  <Link to={`/ledger/entries/${e}`}>{e.slice(0, 8)}</Link>
                </span>
              ))}
            </p>
          ) : null}
          {run!.reversalEntryIds.length > 0 ? (
            <p className={styles.note}>
              Reversing entries:{" "}
              {run!.reversalEntryIds.map((e, i) => (
                <span key={e}>
                  {i > 0 ? ", " : ""}
                  <Link to={`/ledger/entries/${e}`}>{e.slice(0, 8)}</Link>
                </span>
              ))}
            </p>
          ) : null}
          <Actions key={run!.status} run={run!} ledgerId={ledgerId} />
          <Link to={`/ledger/realignments${ledgerQuery(ledgerId)}`}>Back to the runs</Link>
        </>
      )}
    </div>
  );
}
