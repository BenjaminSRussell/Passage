import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  addCashDeskEntry,
  closeCashDeskDay,
  createCashDesk,
  describeProblem,
  getCashDesk,
  listCashDeskDays,
  listCashDeskEntries,
  listCashDesks,
  newKey,
  openCashDeskDay,
  type CashDesk,
  type CashDeskDay,
  type CashDeskEntry,
  type Problem,
} from "./api/client";
import { CURRENCY, DAY, money, SIGNED_DECIMAL, today, UUID } from "./format";
import { CommandFailure, Detail, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

/** Titles for the refusals a cashier can act on (README error table). */
const FAILURES: Record<string, string> = {
  "TRS-0540": "The desk does not hold that much cash",
  "TRS-0541": "The desk would hold more than its maximum",
  "TRS-0542": "The count differs from the books; say why",
  "TRS-0543": "The desk already has an open day",
  "TRS-0544": "The desk has no open day",
  "TRS-0545": "That day is not after the last closed day",
  "TRS-0550": "A cash desk with this code already exists",
};

const POSITIVE = /^\d{1,14}(\.\d{1,6})?$/;

export function validateEntry(kind: string, amount: string, description: string): Errors {
  const errors: Errors = {};
  if (kind !== "receipt" && kind !== "payment") errors.kind = "Choose receipt or payment.";
  if (!POSITIVE.test(amount.trim()) || Number(amount) <= 0) errors.amount = "Enter an amount above zero, such as 25.00.";
  if (description.trim() === "") errors.description = "Say what the cash was for.";
  return errors;
}

export function validateClose(counted: string, book: string, reason: string): Errors {
  const errors: Errors = {};
  if (!POSITIVE.test(counted.trim()) && counted.trim() !== "0") errors.counted = "Enter the cash you counted, such as 379.00. It cannot be negative.";
  else if (SIGNED_DECIMAL.test(book) && Number(counted) !== Number(book) && reason.trim() === "") errors.reason = "The count differs from the books. Say why.";
  return errors;
}

const deskColumns: DataTableColumn<CashDesk>[] = [
  { key: "code", header: "Desk", mono: true },
  { key: "name", header: "Name" },
  { key: "currency", header: "Currency", mono: true },
  { key: "status", header: "Status", render: (d) => <StatusPill status={d.status} /> },
  { key: "openDate", header: "Open day", mono: true, render: (d) => d.openDate ?? "Closed" },
  { key: "balance", header: "Cash on hand", mono: true, align: "end", render: (d) => (d.balance === undefined ? "Not known" : money(d.balance, d.currency)) },
];

/** Cash desks of a legal entity, each with the cash it holds on its open day (FR-36.16). */
export function CashDesksPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const chosen = UUID.test(legalEntityId);
  const desks = useLoad((signal) => (chosen ? listCashDesks(legalEntityId, signal) : Promise.resolve([] as CashDesk[])), legalEntityId);
  return (
    <div className={styles.stack}>
      <PageHeader title="Cash desks" description="Physical cash in the office. Open a day, book receipts and payments, then count the cash and close the day." />
      <Card>
        <div className={styles.toolbar}>
          <FormField id="cd-entity" label="Legal entity">
            {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setParams(e.target.value ? { legalEntityId: e.target.value } : {})} />}
          </FormField>
        </div>
      </Card>
      {!chosen ? (
        <Card>
          <EmptyState icon="database" title="Choose a legal entity" description="Cash desks belong to one legal entity." />
        </Card>
      ) : desks.error ? (
        <LoadFailure title="Could not load the cash desks" problem={desks.error} retry={desks.reload} />
      ) : (
        <>
          <DataTable
            label="Cash desks"
            columns={deskColumns}
            rows={desks.data ?? []}
            getRowKey={(d) => d.id}
            loading={desks.loading && !desks.data}
            empty={{ title: "No cash desks yet", description: "Add the first desk below." }}
            onRowOpen={(d) => navigate(`/treasury/cash-desks/${d.id}`)}
          />
          <NewDeskCard legalEntityId={legalEntityId} onCreated={(d) => navigate(`/treasury/cash-desks/${d.id}`)} />
        </>
      )}
    </div>
  );
}

function NewDeskCard({ legalEntityId, onCreated }: { legalEntityId: string; onCreated(d: CashDesk): void }) {
  const [v, setV] = useState({ code: "", name: "", currency: "USD", mainAccountId: "", maxBalance: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!/^[A-Z0-9-]{1,20}$/.test(v.code.trim())) found.code = "Use 1 to 20 characters: A to Z, 0 to 9 or a hyphen.";
    if (v.name.trim() === "") found.name = "Give the desk a name.";
    if (!CURRENCY.test(v.currency.trim())) found.currency = "Enter an ISO 4217 code such as USD.";
    if (!UUID.test(v.mainAccountId.trim())) found.mainAccountId = "Enter the general ledger cash account (a UUID).";
    if (!POSITIVE.test(v.maxBalance.trim()) || Number(v.maxBalance) <= 0) found.maxBalance = "Enter the most cash the desk may hold, such as 5000.00.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      onCreated(await createCashDesk({ legalEntityId, code: v.code.trim(), name: v.name.trim(), currency: v.currency.trim(), mainAccountId: v.mainAccountId.trim(), maxBalance: v.maxBalance.trim() }, key.current));
    } catch (error) {
      if (!keepKeyAfter(error)) key.current = newKey();
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const set = (name: keyof typeof v) => (e: { target: { value: string } }) => setV((prev) => ({ ...prev, [name]: e.target.value }));
  return (
    <Card title="New cash desk" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="nd-code" label="Code" error={errors.code}>
          {(c) => <Input {...c} className={styles.mono} value={v.code} onChange={set("code")} invalid={Boolean(errors.code)} />}
        </FormField>
        <FormField id="nd-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={v.name} onChange={set("name")} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="nd-currency" label="Currency" error={errors.currency}>
          {(c) => <Input {...c} className={styles.mono} value={v.currency} onChange={set("currency")} invalid={Boolean(errors.currency)} />}
        </FormField>
        <FormField id="nd-account" label="General ledger account id" hint="The cash account the desk belongs to." error={errors.mainAccountId}>
          {(c) => <Input {...c} className={styles.mono} value={v.mainAccountId} onChange={set("mainAccountId")} invalid={Boolean(errors.mainAccountId)} />}
        </FormField>
        <FormField id="nd-max" label="Most cash the desk may hold" error={errors.maxBalance}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={v.maxBalance} onChange={set("maxBalance")} invalid={Boolean(errors.maxBalance)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <CommandFailure title={FAILURES[failure.code] ?? "Could not create the desk"} problem={failure} />
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Create desk
          </Button>
        </div>
      </form>
    </Card>
  );
}

interface DeskView {
  desk: CashDesk;
  days: CashDeskDay[];
}

/** One desk: the open day's journal with its balance, booking and closing, and the days so far. */
export function CashDeskDetailPage() {
  const { id = "" } = useParams();
  return <CashDeskDetail key={id} id={id} />;
}

function CashDeskDetail({ id }: { id: string }) {
  const view = useLoad(async (signal): Promise<DeskView> => ({ desk: await getCashDesk(id, signal), days: await listCashDeskDays(id, signal) }), id);
  const [picked, setPicked] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [newDate, setNewDate] = useState(today());
  const open = view.data?.desk.openDate;
  const date = picked || open || view.data?.days[0]?.businessDate || "";
  const entries = useLoad((signal) => (date ? listCashDeskEntries(id, date, signal) : Promise.resolve([] as CashDeskEntry[])), `${id}|${date}|${view.data?.desk.balance ?? ""}`);

  if (view.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Cash desk" />
        <LoadFailure title="Could not load the cash desk" problem={view.error} retry={view.reload} />
      </div>
    );
  }
  if (!view.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Cash desk" />
        <Card>
          <Skeleton width="3/4" />
        </Card>
      </div>
    );
  }
  const { desk, days } = view.data;

  async function run(action: () => Promise<unknown>, done: string) {
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      await action();
      setNotice(done);
      view.reload();
      entries.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const dayColumns: DataTableColumn<CashDeskDay>[] = [
    { key: "businessDate", header: "Day", mono: true },
    { key: "status", header: "Status", render: (d) => <StatusPill status={d.status} /> },
    { key: "openingBalance", header: "Opening", mono: true, align: "end", render: (d) => money(d.openingBalance, desk.currency) },
    { key: "bookBalance", header: "Books", mono: true, align: "end", render: (d) => money(d.bookBalance, desk.currency) },
    { key: "countedBalance", header: "Counted", mono: true, align: "end", render: (d) => (d.countedBalance === undefined ? "Not counted" : money(d.countedBalance, desk.currency)) },
    { key: "difference", header: "Difference", mono: true, align: "end", render: (d) => (d.difference === undefined ? "None" : money(d.difference, desk.currency)) },
    {
      key: "view",
      header: "Journal",
      render: (d) => (
        <Button size="sm" onClick={() => setPicked(d.businessDate)} aria-label={`Show the journal of ${d.businessDate}`}>
          Show
        </Button>
      ),
    },
  ];
  const entryColumns: DataTableColumn<CashDeskEntry>[] = [
    { key: "lineNo", header: "Line", mono: true, align: "end" },
    { key: "kind", header: "Kind", render: (e) => (e.kind === "receipt" ? "Receipt" : "Payment") },
    { key: "description", header: "For", render: (e) => (e.reference ? `${e.description} (${e.reference})` : e.description) },
    { key: "amount", header: "Amount", mono: true, align: "end", render: (e) => money(e.kind === "payment" ? `-${e.amount}` : e.amount, desk.currency) },
    { key: "balanceAfter", header: "Cash after", mono: true, align: "end", render: (e) => money(e.balanceAfter, desk.currency) },
    { key: "createdBy", header: "By" },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${desk.code}: ${desk.name}`}
        description={open ? `Open day ${open}.` : "No day is open."}
        actions={
          <Link className={buttonClassName("secondary")} to={`/treasury/cash-desks?legalEntityId=${desk.legalEntityId}`}>
            All cash desks
          </Link>
        }
      />
      {notice ? (
        <Alert tone="success" title={notice} live="status">
          The figures below are up to date.
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={FAILURES[failure.code] ?? "That did not work"} problem={failure} /> : null}
      <Card title="Cash on hand" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Cash on hand" mono>
            {desk.balance === undefined ? "No open day" : money(desk.balance, desk.currency)}
          </Detail>
          <Detail term="Most the desk may hold" mono>
            {money(desk.maxBalance, desk.currency)}
          </Detail>
          <Detail term="Status">
            <StatusPill status={desk.status} />
          </Detail>
        </dl>
      </Card>
      {open ? null : (
        <Card title="Open a day" headingLevel={3}>
          <form
            className={styles.toolbar}
            onSubmit={(e) => {
              e.preventDefault();
              if (DAY.test(newDate)) void run(() => openCashDeskDay(id, newDate), "Day opened.");
            }}
          >
            <FormField id="od-date" label="Business date" hint="The day opens with the cash counted at the last close.">
              {(c) => <Input {...c} className={styles.mono} value={newDate} onChange={(e) => setNewDate(e.target.value)} />}
            </FormField>
            <Button type="submit" variant="primary" loading={busy} disabled={!DAY.test(newDate)}>
              Open the day
            </Button>
          </form>
        </Card>
      )}
      <Card title={date ? `Journal of ${date}` : "Journal"} headingLevel={3}>
        {entries.error ? (
          <LoadFailure title="Could not load the journal" problem={entries.error} retry={entries.reload} />
        ) : (
          <DataTable label="Cash journal" columns={entryColumns} rows={entries.data ?? []} getRowKey={(e) => e.id} loading={entries.loading && !entries.data} empty={{ title: "No entries", description: date ? "Nothing was booked on this day." : "Open a day to start." }} />
        )}
      </Card>
      {open && date === open ? (
        <>
          <EntryForm key={`${entries.data?.length ?? 0}`} busy={busy} onBook={(input) => run(() => addCashDeskEntry(id, input), "Entry booked.")} />
          <CloseForm book={desk.balance ?? "0"} currency={desk.currency} busy={busy} onClose={(counted, reason) => run(() => closeCashDeskDay(id, open, counted, reason), "Day closed.")} />
        </>
      ) : null}
      <Card title="Days" headingLevel={3}>
        <DataTable label="Cash desk days" columns={dayColumns} rows={days} getRowKey={(d) => d.businessDate} empty={{ title: "No days yet", description: "Open the first day above." }} />
      </Card>
    </div>
  );
}

function EntryForm({ busy, onBook }: { busy: boolean; onBook(input: { kind: "receipt" | "payment"; amount: string; description: string; reference?: string }): Promise<void> }) {
  const [kind, setKind] = useState<"receipt" | "payment">("receipt");
  const [amount, setAmount] = useState("");
  const [description, setDescription] = useState("");
  const [reference, setReference] = useState("");
  const [errors, setErrors] = useState<Errors>({});

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateEntry(kind, amount, description);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void onBook({ kind, amount: amount.trim(), description: description.trim(), ...(reference.trim() ? { reference: reference.trim() } : {}) });
  }

  return (
    <Card title="Book cash" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="en-kind" label="Kind">
          {(c) => (
            <Select {...c} value={kind} onChange={(e) => setKind(e.target.value as "receipt" | "payment")}>
              <option value="receipt">Receipt: cash in</option>
              <option value="payment">Payment: cash out</option>
            </Select>
          )}
        </FormField>
        <FormField id="en-amount" label="Amount" error={errors.amount}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={amount} onChange={(e) => setAmount(e.target.value)} invalid={Boolean(errors.amount)} />}
        </FormField>
        <FormField id="en-description" label="What it was for" error={errors.description}>
          {(c) => <Input {...c} value={description} onChange={(e) => setDescription(e.target.value)} invalid={Boolean(errors.description)} />}
        </FormField>
        <FormField id="en-reference" label="Reference" optional>
          {(c) => <Input {...c} className={styles.mono} value={reference} onChange={(e) => setReference(e.target.value)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Book it
          </Button>
        </div>
      </form>
    </Card>
  );
}

function CloseForm({ book, currency, busy, onClose }: { book: string; currency: string; busy: boolean; onClose(counted: string, reason: string): Promise<void> }) {
  const [counted, setCounted] = useState("");
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<Errors>({});

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateClose(counted, book, reason);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void onClose(counted.trim(), reason.trim());
  }

  return (
    <Card title="Count and close the day" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>{`The books say ${money(book, currency)}. Count the cash in the desk and enter it.`}</p>
        <FormField id="cl-counted" label="Cash counted" error={errors.counted}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={counted} onChange={(e) => setCounted(e.target.value)} invalid={Boolean(errors.counted)} />}
        </FormField>
        <FormField id="cl-reason" label="Reason for a difference" optional error={errors.reason}>
          {(c) => <Input {...c} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Close the day
          </Button>
        </div>
      </form>
    </Card>
  );
}
