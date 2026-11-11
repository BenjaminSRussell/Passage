import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  addCashDeskEntry,
  approveCashDeskDay,
  closeCashDeskDay,
  createCashDesk,
  describeProblem,
  getCashDesk,
  listBankAccounts,
  listCashDeskDays,
  listCashDeskEntries,
  listCashDesks,
  newKey,
  openCashDeskDay,
  postCashDeskDay,
  rejectCashDeskDay,
  setCashDeskStatus,
  updateCashDesk,
  type BankAccount,
  type CashDesk,
  type CashDeskDay,
  type CashDeskDenomination,
  type CashDeskEntry,
  type CashDeskEntryInput,
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
  "TRS-0515": "The ledger setup does not cover this desk",
  "TRS-0518": "The ledger refused the entry",
  "TRS-0519": "The ledger did not answer",
  "TRS-0552": "Some lines are not in the ledger yet",
  "TRS-0553": "The second signature must come from someone else",
  "TRS-0554": "The day is not waiting for a second signature",
  "TRS-0555": "The desk still has an open day",
  "TRS-0556": "The notes and coins do not add up to the count",
};

const LEDGER_ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const SCALE = 1_000_000;

/** One row of the notes-and-coins count while it is typed. */
export interface CountRow {
  value: string;
  count: string;
}

/** What the notes and coins add up to, as a decimal string with the currency's places, or null while a row is incomplete. */
export function denominationTotal(rows: CountRow[], places = 2): string | null {
  let sum = 0;
  for (const r of rows) {
    if (!POSITIVE.test(r.value.trim()) || Number(r.value) <= 0 || !/^\d{1,7}$/.test(r.count.trim())) return null;
    sum += Math.round(Number(r.value) * SCALE) * Number(r.count);
  }
  return (sum / SCALE).toFixed(places);
}

export function validateCount(rows: CountRow[]): Errors {
  const errors: Errors = {};
  if (rows.length === 0) errors.counted = "Add at least one note or coin.";
  else if (rows.some((r) => !POSITIVE.test(r.value.trim()) || Number(r.value) <= 0)) errors.counted = "Every value must be above zero, such as 20 or 0.25.";
  else if (rows.some((r) => !/^\d{1,7}$/.test(r.count.trim()))) errors.counted = "Every count must be a whole number, zero or more.";
  else if (new Set(rows.map((r) => Number(r.value))).size !== rows.length) errors.counted = "Each value may appear once.";
  return errors;
}

/** An entry needs the ledger account on the other side of the cash, or a bank account for a transfer. */
export function validateCounterAccount(posts: boolean, mode: string, counterAccount: string, bankAccountId: string): Errors {
  if (mode === "bank") return UUID.test(bankAccountId) ? {} : { bankAccountId: "Choose the bank account." };
  if (counterAccount.trim() === "") return posts ? { counterAccount: "Enter the ledger account on the other side of the cash." } : {};
  return LEDGER_ACCOUNT.test(counterAccount.trim()) ? {} : { counterAccount: "Use 1 to 20 letters, digits, dots, hyphens or underscores." };
}

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
  const [v, setV] = useState({ code: "", name: "", currency: "USD", mainAccountId: "", maxBalance: "", dualControl: "no" });
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
    if (!UUID.test(v.mainAccountId.trim())) found.mainAccountId = "Choose the general ledger cash account.";
    if (!POSITIVE.test(v.maxBalance.trim()) || Number(v.maxBalance) <= 0) found.maxBalance = "Enter the most cash the desk may hold, such as 5000.00.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      onCreated(await createCashDesk({ legalEntityId, code: v.code.trim(), name: v.name.trim(), currency: v.currency.trim(), mainAccountId: v.mainAccountId.trim(), maxBalance: v.maxBalance.trim(), ...(v.dualControl === "yes" ? { dualControl: true } : {}) }, key.current));
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
        <FormField id="nd-account" label="General ledger account" hint="The cash account the desk belongs to." error={errors.mainAccountId}>
          {(c) => <EntityLookup {...c} entity="mainAccount" scope={{ legalEntityId }} value={v.mainAccountId} onChange={(e) => setV((prev) => ({ ...prev, mainAccountId: e.target.value }))} invalid={Boolean(errors.mainAccountId)} />}
        </FormField>
        <FormField id="nd-max" label="Most cash the desk may hold" error={errors.maxBalance}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={v.maxBalance} onChange={set("maxBalance")} invalid={Boolean(errors.maxBalance)} />}
        </FormField>
        <FormField id="nd-dual" label="Closing a day" hint="With a second signature, a counted day stays open until another person approves it.">
          {(c) => (
            <Select {...c} value={v.dualControl} onChange={set("dualControl")}>
              <option value="no">One person counts and closes</option>
              <option value="yes">A second person must sign</option>
            </Select>
          )}
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
  const legalEntityId = view.data?.desk.legalEntityId ?? "";
  const banks = useLoad((signal) => (legalEntityId ? listBankAccounts(legalEntityId, signal) : Promise.resolve([] as BankAccount[])), legalEntityId);

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
  const openDay = days.find((d) => d.businessDate === open);
  const counted = openDay?.status === "closing";
  const waiting = (entries.data ?? []).filter((e) => e.postingStatus === "pending").length;
  const posts = (entries.data ?? []).some((e) => e.postingStatus === "pending" || e.postingStatus === "posted");

  async function run(action: () => Promise<unknown>, done: string) {
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      await action();
      setNotice(done);
    } catch (error) {
      // A refused posting leaves the line saved and waiting, so the figures still change.
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
      view.reload();
      entries.reload();
    }
  }

  const dayColumns: DataTableColumn<CashDeskDay>[] = [
    { key: "businessDate", header: "Day", mono: true },
    { key: "status", header: "Status", render: (d) => (d.status === "closing" ? "Waiting for a second signature" : <StatusPill status={d.status} />) },
    { key: "openingBalance", header: "Opening", mono: true, align: "end", render: (d) => money(d.openingBalance, desk.currency) },
    { key: "bookBalance", header: "Books", mono: true, align: "end", render: (d) => money(d.bookBalance, desk.currency) },
    { key: "countedBalance", header: "Counted", mono: true, align: "end", render: (d) => (d.countedBalance === undefined ? "Not counted" : money(d.countedBalance, desk.currency)) },
    { key: "difference", header: "Difference", mono: true, align: "end", render: (d) => (d.difference === undefined ? "None" : money(d.difference, desk.currency)) },
    { key: "signed", header: "Signed by", render: (d) => (d.approvedBy ? `${d.closedBy} and ${d.approvedBy}` : (d.closedBy ?? "")) },
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
    { key: "counter", header: "Other side", render: (e) => (e.bankAccountId ? (banks.data?.find((b) => b.id === e.bankAccountId)?.accountCode ?? "Bank account") : (e.counterAccount ?? "")) },
    { key: "amount", header: "Amount", mono: true, align: "end", render: (e) => money(e.kind === "payment" ? `-${e.amount}` : e.amount, desk.currency) },
    { key: "balanceAfter", header: "Cash after", mono: true, align: "end", render: (e) => money(e.balanceAfter, desk.currency) },
    { key: "posting", header: "Ledger", render: (e) => (e.postingStatus === "posted" ? <StatusPill status="posted" /> : e.postingStatus === "pending" ? <StatusPill status="pending" /> : "Not posted") },
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
      {waiting > 0 ? (
        <Alert tone="warning" title={`${waiting} ${waiting === 1 ? "line is" : "lines are"} saved but not in the ledger yet`}>
          The day cannot be closed until the ledger has them. Posting again is safe: nothing is booked twice.
          <div>
            <Button size="sm" loading={busy} onClick={() => void run(() => postCashDeskDay(id, date), "Posted to the ledger.")}>
              Post to the ledger
            </Button>
          </div>
        </Alert>
      ) : null}
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
          <Detail term="Closing a day">{desk.dualControl ? "A second person must sign" : "One person counts and closes"}</Detail>
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
            <Button type="submit" variant="primary" loading={busy} disabled={!DAY.test(newDate) || desk.status !== "active"}>
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
      {open && date === open && !counted ? (
        <>
          <EntryForm key={`${entries.data?.length ?? 0}`} desk={desk} banks={banks.data ?? []} posts={posts} busy={busy} onBook={(input) => run(() => addCashDeskEntry(id, input), "Entry booked.")} />
          <CloseForm
            book={desk.balance ?? "0"}
            currency={desk.currency}
            busy={busy}
            dual={Boolean(desk.dualControl)}
            onClose={(count, reason) => run(() => closeCashDeskDay(id, open, count, reason), desk.dualControl ? "Counted. The day waits for a second signature." : "Day closed.")}
          />
        </>
      ) : null}
      {open && counted && openDay ? (
        <Card title="Second signature" headingLevel={3}>
          <p className={styles.note}>{`${openDay.closedBy ?? "Someone"} counted ${money(openDay.countedBalance ?? "0", desk.currency)} against books of ${money(openDay.bookBalance, desk.currency)}${openDay.differenceReason ? ` (${openDay.differenceReason})` : ""}. Another person approves the count to close the day, or sends it back to be recounted.`}</p>
          <div className={styles.actions}>
            <Button variant="primary" loading={busy} onClick={() => void run(() => approveCashDeskDay(id, open), "Day closed.")}>
              Approve and close the day
            </Button>
            <Button loading={busy} onClick={() => void run(() => rejectCashDeskDay(id, open), "Sent back to be recounted.")}>
              Send back for a recount
            </Button>
          </div>
        </Card>
      ) : null}
      <EditDeskCard key={desk.version} desk={desk} busy={busy} onSave={(changes) => run(() => updateCashDesk(desk, changes), "Desk saved.")} onStatus={(status) => run(() => setCashDeskStatus(desk, status), status === "active" ? "Desk activated." : "Desk deactivated.")} />
      <Card title="Days" headingLevel={3}>
        <DataTable label="Cash desk days" columns={dayColumns} rows={days} getRowKey={(d) => d.businessDate} empty={{ title: "No days yet", description: "Open the first day above." }} />
      </Card>
    </div>
  );
}

function EditDeskCard({ desk, busy, onSave, onStatus }: { desk: CashDesk; busy: boolean; onSave(changes: { name?: string; maxBalance?: string; dualControl?: boolean }): Promise<void>; onStatus(status: "active" | "inactive"): Promise<void> }) {
  const [name, setName] = useState(desk.name);
  const [max, setMax] = useState(desk.maxBalance);
  const [dual, setDual] = useState(desk.dualControl ? "yes" : "no");
  const [errors, setErrors] = useState<Errors>({});

  function submit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (name.trim() === "") found.name = "Give the desk a name.";
    if (!POSITIVE.test(max.trim()) || Number(max) <= 0) found.maxBalance = "Enter the most cash the desk may hold, such as 5000.00.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void onSave({ ...(name.trim() !== desk.name ? { name: name.trim() } : {}), ...(max.trim() !== desk.maxBalance ? { maxBalance: max.trim() } : {}), ...((dual === "yes") !== Boolean(desk.dualControl) ? { dualControl: dual === "yes" } : {}) });
  }

  return (
    <Card title="Desk settings" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="ed-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={name} onChange={(e) => setName(e.target.value)} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="ed-max" label="Most cash the desk may hold" error={errors.maxBalance}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={max} onChange={(e) => setMax(e.target.value)} invalid={Boolean(errors.maxBalance)} />}
        </FormField>
        <FormField id="ed-dual" label="Closing a day">
          {(c) => (
            <Select {...c} value={dual} onChange={(e) => setDual(e.target.value)}>
              <option value="no">One person counts and closes</option>
              <option value="yes">A second person must sign</option>
            </Select>
          )}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save the desk
          </Button>
          {desk.status === "active" ? (
            <Button loading={busy} onClick={() => void onStatus("inactive")} disabled={Boolean(desk.openDate)}>
              Deactivate the desk
            </Button>
          ) : (
            <Button loading={busy} onClick={() => void onStatus("active")}>
              Activate the desk
            </Button>
          )}
        </div>
        {desk.status === "active" && desk.openDate ? <p className={styles.note}>Close the open day before deactivating the desk.</p> : null}
      </form>
    </Card>
  );
}

function EntryForm({ desk, banks, posts, busy, onBook }: { desk: CashDesk; banks: BankAccount[]; posts: boolean; busy: boolean; onBook(input: CashDeskEntryInput): Promise<void> }) {
  const [kind, setKind] = useState<"receipt" | "payment">("receipt");
  const [amount, setAmount] = useState("");
  const [description, setDescription] = useState("");
  const [reference, setReference] = useState("");
  const [mode, setMode] = useState<"account" | "bank">("account");
  const [counterAccount, setCounterAccount] = useState("");
  const [bankAccountId, setBankAccountId] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const eligible = banks.filter((b) => b.currency === desk.currency && b.status === "active");

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = { ...validateEntry(kind, amount, description), ...validateCounterAccount(posts, mode, counterAccount, bankAccountId) };
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void onBook({
      kind,
      amount: amount.trim(),
      description: description.trim(),
      ...(reference.trim() ? { reference: reference.trim() } : {}),
      ...(mode === "bank" ? { bankAccountId } : counterAccount.trim() ? { counterAccount: counterAccount.trim() } : {}),
    });
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
        <FormField id="en-mode" label="Other side of the cash" hint="A payment into a bank account, or cash drawn from one, is a bank transfer.">
          {(c) => (
            <Select {...c} value={mode} onChange={(e) => setMode(e.target.value as "account" | "bank")}>
              <option value="account">A ledger account</option>
              <option value="bank">A bank account (transfer)</option>
            </Select>
          )}
        </FormField>
        {mode === "bank" ? (
          <FormField id="en-bank" label="Bank account" error={errors.bankAccountId} hint={eligible.length === 0 ? `No active ${desk.currency} bank account in this legal entity.` : undefined}>
            {(c) => (
              <Select {...c} value={bankAccountId} onChange={(e) => setBankAccountId(e.target.value)} invalid={Boolean(errors.bankAccountId)}>
                <option value="">Choose a bank account</option>
                {eligible.map((b) => (
                  <option key={b.id} value={b.id}>{`${b.accountCode} (${b.ibanMasked})`}</option>
                ))}
              </Select>
            )}
          </FormField>
        ) : (
          <FormField id="en-counter" label="Ledger account" optional={!posts} hint="Required when the desk posts to the ledger." error={errors.counterAccount}>
            {(c) => <Input {...c} className={styles.mono} value={counterAccount} onChange={(e) => setCounterAccount(e.target.value)} invalid={Boolean(errors.counterAccount)} />}
          </FormField>
        )}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Book it
          </Button>
        </div>
      </form>
    </Card>
  );
}

function CloseForm({ book, currency, busy, dual, onClose }: { book: string; currency: string; busy: boolean; dual: boolean; onClose(count: { countedBalance?: string; denominations?: CashDeskDenomination[] }, reason: string): Promise<void> }) {
  const [by, setBy] = useState<"total" | "coins">("total");
  const [counted, setCounted] = useState("");
  const [rows, setRows] = useState<CountRow[]>([{ value: "", count: "" }]);
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const total = by === "coins" ? denominationTotal(rows) : counted.trim();

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = by === "coins" ? validateCount(rows) : validateClose(counted, book, reason);
    if (by === "coins" && Object.keys(found).length === 0) Object.assign(found, validateClose(total ?? "", book, reason));
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void onClose(by === "coins" ? { denominations: rows.map((r) => ({ value: r.value.trim(), count: Number(r.count) })) } : { countedBalance: counted.trim() }, reason.trim());
  }
  const setRow = (i: number, patch: Partial<CountRow>) => setRows((prev) => prev.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  return (
    <Card title="Count and close the day" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>{`The books say ${money(book, currency)}. Count the cash in the desk and enter it.${dual ? " Another person signs the count before the day closes." : ""}`}</p>
        <FormField id="cl-by" label="Count by">
          {(c) => (
            <Select {...c} value={by} onChange={(e) => setBy(e.target.value as "total" | "coins")}>
              <option value="total">One total</option>
              <option value="coins">Notes and coins</option>
            </Select>
          )}
        </FormField>
        {by === "total" ? (
          <FormField id="cl-counted" label="Cash counted" error={errors.counted}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={counted} onChange={(e) => setCounted(e.target.value)} invalid={Boolean(errors.counted)} />}
          </FormField>
        ) : (
          <fieldset className={styles.form}>
            <legend>Notes and coins</legend>
            {rows.map((r, i) => (
              <div className={styles.toolbar} key={i}>
                <FormField id={`cl-value-${i}`} label={`Value ${i + 1}`}>
                  {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={r.value} onChange={(e) => setRow(i, { value: e.target.value })} />}
                </FormField>
                <FormField id={`cl-count-${i}`} label={`How many of value ${i + 1}`}>
                  {(c) => <Input {...c} className={styles.mono} inputMode="numeric" value={r.count} onChange={(e) => setRow(i, { count: e.target.value })} />}
                </FormField>
                <Button size="sm" onClick={() => setRows((prev) => prev.filter((_, j) => j !== i))} disabled={rows.length === 1} aria-label={`Remove value ${i + 1}`}>
                  Remove
                </Button>
              </div>
            ))}
            {errors.counted ? <p role="alert">{errors.counted}</p> : null}
            <div className={styles.actions}>
              <Button size="sm" onClick={() => setRows((prev) => [...prev, { value: "", count: "" }])} disabled={rows.length >= 40}>
                Add a value
              </Button>
              <span className={styles.mono}>{total === null ? "Total: complete every row" : `Total: ${money(total, currency)}`}</span>
            </div>
          </fieldset>
        )}
        <FormField id="cl-reason" label="Reason for a difference" optional error={errors.reason}>
          {(c) => <Input {...c} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            {dual ? "Count the day" : "Close the day"}
          </Button>
        </div>
      </form>
    </Card>
  );
}
