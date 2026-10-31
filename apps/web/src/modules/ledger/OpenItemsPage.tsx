import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  createClearing,
  fetchOpenItems,
  fetchToleranceGroups,
  LedgerApiError,
  newKey,
  OPEN_ITEM_STATUSES,
  simulateClearing,
  type ClearingInput,
  type ClearingPreview,
  type OpenItem,
} from "./api/client";
import { formatDimensions } from "./LedgerLinesPage";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import styles from "./pages.module.css";

const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

const itemTone: Record<OpenItem["status"], { label: string; tone: BadgeTone }> = {
  open: { label: "Open", tone: "info" },
  reset_open: { label: "Open again", tone: "info" },
  cleared: { label: "Cleared", tone: "success" },
  voided: { label: "Voided", tone: "neutral" },
};

export function ItemStatusBadge({ status }: { status: OpenItem["status"] }) {
  const t = itemTone[status];
  return <Badge tone={t.tone}>{t.label}</Badge>;
}

export function errorText(error: unknown): string {
  if (error instanceof LedgerApiError) return `${error.message} (${error.code})`;
  return (error as Error).message;
}

/** What a preview means for the user, in plain words. */
export function outcomeText(p: ClearingPreview): string {
  switch (p.outcome) {
    case "balanced":
      return `The ${p.itemCount} items net to zero and clear exactly.`;
    case "difference":
      return `The items leave ${p.net} ${p.currency}. It is within tolerance group ${p.toleranceGroup ?? ""} and is written off to its difference account.`;
    case "residual":
      return `The items leave ${p.net} ${p.currency}, above tolerance. It carries on as a new open item.`;
    default:
      return `The items leave ${p.net} ${p.currency}. Add items, name a tolerance group that covers it, or create a residual item.`;
  }
}

function outcomeTone(outcome: ClearingPreview["outcome"]): "success" | "warning" | "danger" {
  if (outcome === "balanced" || outcome === "difference") return "success";
  return outcome === "residual" ? "warning" : "danger";
}

const today = () => new Date().toISOString().slice(0, 10);

function ClearPanel({ ledgerId, itemIds, onCleared }: { ledgerId: string; itemIds: string[]; onCleared: (id: string) => void }) {
  const [date, setDate] = useState(today());
  const [group, setGroup] = useState("");
  const [residual, setResidual] = useState(false);
  const [preview, setPreview] = useState<ClearingPreview | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState<"preview" | "clear" | null>(null);
  const attempt = useRef<{ signature: string; key: string }>({ signature: "", key: "" });
  const groups = useQuery({ queryKey: ["ledger", "tolerance-groups", ledgerId], queryFn: () => fetchToleranceGroups(ledgerId) });

  const input = (): ClearingInput => ({ ledgerId, itemIds, clearingDate: date, ...(group ? { toleranceGroup: group } : {}), residualItem: residual });

  function check(): boolean {
    if (!DATE.test(date)) {
      setError("Dates use the form YYYY-MM-DD.");
      return false;
    }
    setError(undefined);
    return true;
  }

  async function run(kind: "preview" | "clear", event?: FormEvent) {
    event?.preventDefault();
    setFailure(null);
    if (!check()) return;
    setBusy(kind);
    try {
      if (kind === "preview") {
        setPreview(await simulateClearing(input()));
      } else {
        // One key per intended clearing: a retry of the same selection replays instead of clearing twice.
        const signature = JSON.stringify(input());
        if (attempt.current.signature !== signature) attempt.current = { signature, key: newKey() };
        const doc = await createClearing(input(), attempt.current.key);
        onCleared(doc.id);
      }
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void run("preview", e)} noValidate aria-label="Clear the selected items">
        <FormField id="oi-date" label="Clearing date" hint="Not before the latest posting date of the items." error={error}>
          {(c) => <Input {...c} className={styles.mono} value={date} onChange={(e) => { setDate(e.target.value); setPreview(null); }} invalid={Boolean(error)} />}
        </FormField>
        <FormField id="oi-group" label="Tolerance group" hint="Writes a small residual off to its difference account.">
          {(c) => (
            <Select {...c} value={group} onChange={(e) => { setGroup(e.target.value); setPreview(null); }}>
              <option value="">None</option>
              {(groups.data ?? []).filter((g) => g.active).map((g) => (
                <option key={g.code} value={g.code}>{`${g.code} - debit up to ${g.debitLimit}, credit up to ${g.creditLimit}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <span className={styles.check}>
          <Input id="oi-residual" type="checkbox" checked={residual} onChange={(e) => { setResidual(e.target.checked); setPreview(null); }} />
          <label htmlFor="oi-residual">Carry a larger residual as a new open item</label>
        </span>
        {groups.isError ? (
          <div className={styles.wide}>
            <Alert tone="warning" title="Could not load the tolerance groups" live="status">{errorText(groups.error)}</Alert>
          </div>
        ) : null}
        {preview ? (
          <div className={styles.wide}>
            <Alert tone={outcomeTone(preview.outcome)} title={`Preview: ${preview.outcome}`} live="status">
              {outcomeText(preview)} Debit {preview.debit}, credit {preview.credit}, difference {preview.net} {preview.currency}.
            </Alert>
          </div>
        ) : null}
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not clear the items" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy === "preview"}>Preview</Button>
          <Button type="button" variant="primary" loading={busy === "clear"} onClick={() => void run("clear")}>
            {`Clear ${itemIds.length} items`}
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Open items of open-item managed accounts, with selection and manual clearing (FR-11.19). */
export function OpenItemsPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [ledgerId, setLedgerId] = useLedgerParam();
  const [draft, setDraft] = useState({ account: "", status: "open", assignment: "" });
  const [applied, setApplied] = useState(draft);
  const [errors, setErrors] = useState<{ account?: string }>({});
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const query = ledgerQuery(ledgerId);

  const result = useInfiniteQuery({
    queryKey: ["ledger", "open-items", ledgerId, applied],
    queryFn: ({ pageParam }) => fetchOpenItems({ ledgerId, ...applied, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    enabled: ledgerId !== "",
  });
  const rows = result.data?.pages.flatMap((p) => p.items) ?? [];
  const clearable = (r: OpenItem) => r.status === "open" || r.status === "reset_open";

  function apply(event: FormEvent) {
    event.preventDefault();
    if (draft.account && !ACCOUNT.test(draft.account)) {
      setErrors({ account: "Up to 20 letters, digits, dots, underscores or dashes." });
      return;
    }
    setErrors({});
    setSelected(new Set());
    setApplied(draft);
  }

  const toggle = (id: string, on: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  const columns: DataTableColumn<OpenItem>[] = [
    {
      key: "select",
      header: "Select",
      render: (r) =>
        clearable(r) ? (
          <Input type="checkbox" aria-label={`Select item ${r.voucher ?? r.id}`} checked={selected.has(r.id)} onChange={(e) => toggle(r.id, e.target.checked)} />
        ) : null,
    },
    { key: "account", header: "Account", mono: true },
    { key: "postingDate", header: "Posting date", mono: true },
    { key: "voucher", header: "Voucher", mono: true, render: (r) => (r.entryId ? <Link to={`/ledger/entries/${r.entryId}`}>{r.voucher}</Link> : "Residual") },
    { key: "assignment", header: "Assignment", mono: true },
    { key: "dimensions", header: "Dimensions", mono: true, render: (r) => formatDimensions(r.dimensions) },
    { key: "debit", header: "Debit", align: "end", mono: true, render: (r) => (r.debitCredit === "D" ? r.amount : "") },
    { key: "credit", header: "Credit", align: "end", mono: true, render: (r) => (r.debitCredit === "C" ? r.amount : "") },
    { key: "status", header: "Status", render: (r) => <ItemStatusBadge status={r.status} /> },
    {
      key: "clearingDocumentId",
      header: "Clearing",
      render: (r) => (r.clearingDocumentId ? <Link to={`/ledger/clearings/${r.clearingDocumentId}${query}`}>Document</Link> : ""),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Open items"
        description="Lines posted on open-item managed accounts stay open until they are cleared against each other. Select two or more items of one account to clear them."
        actions={
          ledgerId ? (
            <Link className={buttonClassName("secondary")} to={`/ledger/clearings${query}`}>
              Clearing documents
            </Link>
          ) : undefined
        }
      />
      <LedgerPicker id="oi-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={(id) => { setSelected(new Set()); setLedgerId(id); }} />
      {ledgerId === "" ? (
        <p className={styles.note}>Enter a ledger id to see its open items.</p>
      ) : (
        <>
          <Card>
            <form className={styles.form} onSubmit={apply} noValidate aria-label="Filter open items">
              <FormField id="oi-account" label="Account" error={errors.account}>
                {(c) => <Input {...c} className={styles.mono} value={draft.account} onChange={(e) => setDraft({ ...draft, account: e.target.value })} invalid={Boolean(errors.account)} />}
              </FormField>
              <FormField id="oi-status" label="Status">
                {(c) => (
                  <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value })}>
                    <option value="">All</option>
                    {OPEN_ITEM_STATUSES.map((s) => (
                      <option key={s} value={s}>{s === "open" ? "Open (incl. open again)" : s === "reset_open" ? "Open again" : s[0].toUpperCase() + s.slice(1)}</option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="oi-assignment" label="Assignment">
                {(c) => <Input {...c} className={styles.mono} maxLength={50} value={draft.assignment} onChange={(e) => setDraft({ ...draft, assignment: e.target.value })} />}
              </FormField>
              <div className={styles.formActions}>
                <Button type="submit" variant="primary">Apply filters</Button>
              </div>
            </form>
          </Card>
          {result.isError && !result.isFetchNextPageError ? (
            <Alert tone="danger" title="Could not load the open items" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
              {errorText(result.error)}
            </Alert>
          ) : (
            <>
              <DataTable
                label="Open items"
                columns={columns}
                rows={rows}
                getRowKey={(r) => r.id}
                loading={result.isPending}
                empty={{ title: "No open items", description: "Switch an account to open-item management, or change the filters." }}
              />
              {result.isFetchNextPageError ? (
                <Alert tone="danger" title="Could not load more items" live="alert" action={<Button size="sm" onClick={() => void result.fetchNextPage()}>Try again</Button>}>
                  {errorText(result.error)}
                </Alert>
              ) : null}
              {result.hasNextPage && !result.isFetchNextPageError ? (
                <div className={styles.formActions}>
                  <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>Load more items</Button>
                  <span className={styles.note}>{rows.length} items shown</span>
                </div>
              ) : null}
            </>
          )}
          {selected.size >= 2 ? (
            <ClearPanel
              key={ledgerId}
              ledgerId={ledgerId}
              itemIds={[...selected]}
              onCleared={(id) => {
                void queryClient.invalidateQueries({ queryKey: ["ledger", "open-items"] });
                void queryClient.invalidateQueries({ queryKey: ["ledger", "clearings"] });
                navigate(`/ledger/clearings/${id}${query}`);
              }}
            />
          ) : (
            <p className={styles.note}>Select at least two open items to clear them.</p>
          )}
          <p className={styles.note}>
            <Link to={`/ledger/open-item-accounts${query}`}>Open-item accounts</Link> · <Link to={`/ledger/tolerance-groups${query}`}>Tolerance groups</Link>
          </p>
        </>
      )}
    </div>
  );
}

