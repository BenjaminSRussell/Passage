import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import type { LookupEntityKey } from "../../shell/lookup/entities";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
  ListPage,
  type ListColumn,
  type ListData,
  type ListFilterDef,
  type ListQuery,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import {
  createDownPayment,
  describeProblem,
  DOWN_PAYMENT_KINDS,
  DOWN_PAYMENT_STATUSES,
  getDownPayment,
  listDownPayments,
  moveDownPayment,
  newKey,
  type DownPayment,
  type DownPaymentAction,
  type DownPaymentClearing,
  type DownPaymentInput,
  type DownPaymentKind,
  type DownPaymentStatus,
  type Problem,
} from "./api/client";
import {
  compareDecimals,
  CURRENCY,
  DAY,
  dateTime,
  DECIMAL,
  DOWN_PAYMENT_LABELS,
  DOWN_PAYMENT_TONES,
  KIND_LABELS,
  money,
  today,
  UUID,
} from "./format";
import { CommandFailure, Detail, isStale, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useCappedRows, useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";

const PAGE = 500;
const ACCOUNT: Record<DownPaymentKind, string> = { DOWN_PAYMENT: "2190", SECURITY_DEPOSIT: "2195" };

function statusBadge(status: DownPaymentStatus) {
  return <Badge tone={DOWN_PAYMENT_TONES[status] ?? "neutral"}>{DOWN_PAYMENT_LABELS[status] ?? status}</Badge>;
}

const columns: ListColumn<DownPayment>[] = [
  { key: "kind", header: "Kind", render: (row) => KIND_LABELS[row.kind] ?? row.kind },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "remaining", header: "Remaining", mono: true, align: "end", render: (row) => money(row.remaining, row.currency) },
  { key: "receivedDate", header: "Received", mono: true, render: (row) => row.receivedDate ?? "Not yet" },
  { key: "status", header: "Status", render: (row) => statusBadge(row.status) },
];

const downPaymentFilters: ListFilterDef[] = [
  { kind: "select", key: "kind", label: "Kind", anyLabel: "All kinds", options: DOWN_PAYMENT_KINDS.map((k) => ({ value: k, label: KIND_LABELS[k] })) },
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: DOWN_PAYMENT_STATUSES.map((s) => ({ value: s, label: DOWN_PAYMENT_LABELS[s] })) },
];

function useDownPaymentRows(query: ListQuery, onCapped: (capped: boolean) => void): ListData<DownPayment> {
  const kind = (query.filters.kind ?? "") as DownPaymentKind | "";
  const status = (query.filters.status ?? "") as DownPaymentStatus | "";
  return useCappedRows((signal) => listDownPayments({ kind, status, limit: PAGE }, signal), `${kind}|${status}`, PAGE, onCapped);
}

/** Down payments and security deposits (FR-30.21), newest first. */
export function DownPaymentsPage() {
  const navigate = useNavigate();
  const [capped, setCapped] = useState(false);
  return (
    <ListPage
      id="receivables-down-payments"
      title="Down payments and deposits"
      description="Down payments (2190) clear the order's final invoice. Security deposits (2195) offset open items or are refunded."
      actions={
        <Link className={buttonClassName("primary")} to="/receivables/down-payments/new">
          Request down payment
        </Link>
      }
      noun="down payments"
      singular="down payment"
      columns={columns}
      filters={downPaymentFilters}
      useRows={(query) => useDownPaymentRows(query, setCapped)}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/receivables/down-payments/${row.id}`)}
      empty={{ title: "No down payments", description: "Request a down payment for an order, or a security deposit from a customer." }}
      footer={capped ? <p className={styles.note}>Showing the first {PAGE} down payments. Narrow the filters to see the others.</p> : null}
    />
  );
}

type Errors = Record<string, string>;

export interface DownPaymentDraft {
  legalEntityId: string;
  customerId: string;
  salesOrderId: string;
  kind: DownPaymentKind;
  currency: string;
  amount: string;
}

/** Client checks that mirror the service validation; the service has the last word. */
export function validateDownPayment(v: DownPaymentDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!UUID.test(v.customerId.trim())) errors.customerId = "Choose a customer.";
  const order = v.salesOrderId.trim();
  if (order !== "" && !UUID.test(order)) errors.salesOrderId = "Enter the sales order id (a UUID) or leave it empty.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a currency such as USD.";
  if (!DECIMAL.test(v.amount.trim()) || /^0+(\.0+)?$/.test(v.amount.trim())) errors.amount = "Enter an amount above zero, such as 2000.00.";
  return errors;
}

export function toDownPaymentInput(v: DownPaymentDraft): DownPaymentInput {
  const order = v.salesOrderId.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    customerId: v.customerId.trim(),
    salesOrderId: order === "" ? null : order,
    kind: v.kind,
    currency: v.currency.trim(),
    amount: v.amount.trim(),
  };
}

const NEW_DOWN_PAYMENT_FIELDS: Record<string, FormFieldTarget> = {
  legalEntityId: { id: "nd-entity", label: "Legal entity" },
  customerId: { id: "nd-customer", label: "Customer" },
  salesOrderId: { id: "nd-order", label: "Sales order" },
  currency: { id: "nd-currency", label: "Currency" },
  amount: { id: "nd-amount", label: "Amount" },
};

/** Request a down payment or a security deposit. Nothing posts until the money is received. */
export function NewDownPaymentPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<DownPaymentDraft>({
    legalEntityId: params.get("legalEntity") ?? "",
    customerId: params.get("customer") ?? "",
    salesOrderId: params.get("salesOrder") ?? "",
    kind: params.get("kind") === "SECURITY_DEPOSIT" ? "SECURITY_DEPOSIT" : "DOWN_PAYMENT",
    currency: "",
    amount: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  function update(patch: Partial<DownPaymentDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateDownPayment(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createDownPayment(toDownPaymentInput(values), key.current);
      release();
      navigate(`/receivables/down-payments/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: Exclude<keyof DownPaymentDraft, "kind">, label: string, opts: { entity?: LookupEntityKey; hint?: string; optional?: boolean; decimal?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} error={errors[field]} optional={opts.optional}>
        {(c) =>
        opts.entity ? (
          <EntityLookup
            {...c}
            entity={opts.entity}
            scope={opts.entity === "customer" ? { legalEntityId: values.legalEntityId || undefined } : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<DownPaymentDraft>)}
            invalid={Boolean(errors[field])}
          />
        ) : (
          <Input
            {...c}
            className={styles.mono}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<DownPaymentDraft>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Request down payment" description="A request is a noted item: it posts nothing until you record the money as received." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormErrorSummary items={summaryItems(errors, NEW_DOWN_PAYMENT_FIELDS)} attempt={attempt} />
          <FormField id="nd-kind" label="Kind">
            {(c) => (
              <Select {...c} value={values.kind} onChange={(e) => update({ kind: e.target.value as DownPaymentKind })}>
                {DOWN_PAYMENT_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {`${KIND_LABELS[k]} (${ACCOUNT[k]})`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("nd-entity", "legalEntityId", "Legal entity", { entity: "legalEntity" })}
          {text("nd-customer", "customerId", "Customer", { entity: "customer" })}
          {text("nd-order", "salesOrderId", "Sales order", { entity: "salesOrder", optional: true })}
          {text("nd-currency", "currency", "Currency")}
          {text("nd-amount", "amount", "Amount", { decimal: true })}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not record the request" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record request
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/down-payments">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}

const clearingColumns: DataTableColumn<DownPaymentClearing>[] = [
  { key: "documentNumber", header: "Item", mono: true },
  { key: "amount", header: "Cleared", mono: true, align: "end", render: (row) => row.amount },
  { key: "clearedAt", header: "When", mono: true, render: (row) => dateTime(row.clearedAt) },
];

/** Checks the clearing form: an item id and, when given, an amount within what remains. */
export function validateClearing(openItemId: string, amount: string, remaining: string): Errors {
  const errors: Errors = {};
  if (!UUID.test(openItemId.trim())) errors.openItemId = "Enter the open item id (a UUID).";
  const text = amount.trim();
  if (text !== "") {
    if (!DECIMAL.test(text) || /^0+(\.0+)?$/.test(text)) errors.amount = "Enter an amount above zero, or leave it empty to clear the most possible.";
    else if (compareDecimals(text, remaining) > 0) errors.amount = `At most ${remaining} remains.`;
  }
  return errors;
}

type Busy = DownPaymentAction["action"] | null;

const DONE: Record<DownPaymentAction["action"], string> = {
  receive: "Recorded as received.",
  cancel: "The request is cancelled.",
  clear: "Cleared against the item.",
  release: "The deposit's remainder is released for refund.",
};

/** One down payment or deposit: receive it, clear it against items, or refund a deposit. */
export function DownPaymentDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getDownPayment(id, signal), id);
  const [receivedDate, setReceivedDate] = useState(today());
  const [openItemId, setOpenItemId] = useState("");
  const [amount, setAmount] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState<Busy>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Down payment" />
        <LoadFailure title="Could not load the down payment" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Down payment" />
        <span className="visually-hidden">Loading the down payment…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const dp = state.data;
  const account = ACCOUNT[dp.kind];

  async function run(command: DownPaymentAction) {
    setBusy(command.action);
    setFailure(null);
    setDone(null);
    try {
      await moveDownPayment(dp.id, dp.version, command);
      setDone(DONE[command.action]);
      setOpenItemId("");
      setAmount("");
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure({
        title: isStale(error) ? "The down payment changed since you opened it. Reload it and try again." : "Could not update the down payment",
        problem,
      });
    } finally {
      setBusy(null);
    }
  }

  function receive(event: FormEvent) {
    event.preventDefault();
    if (!DAY.test(receivedDate)) {
      setErrors({ receivedDate: "Enter the day the money arrived." });
      return;
    }
    setErrors({});
    void run({ action: "receive", receivedDate });
  }

  function clear(event: FormEvent) {
    event.preventDefault();
    const found = validateClearing(openItemId, amount, dp.remaining);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const text = amount.trim();
    void run(text === "" ? { action: "clear", openItemId: openItemId.trim() } : { action: "clear", openItemId: openItemId.trim(), amount: text });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${KIND_LABELS[dp.kind]} ${money(dp.amount, dp.currency)}`}
        description={
          dp.kind === "DOWN_PAYMENT"
            ? `Received money sits on ${account} until it clears the order's final invoice (Dr ${account} / Cr 1200).`
            : `Received money sits on ${account}; offset it against open items or refund what remains.`
        }
        meta={statusBadge(dp.status)}
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/down-payments">
            All down payments
          </Link>
        }
      />
      <Card title="Details" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Amount" mono>{money(dp.amount, dp.currency)}</Detail>
          <Detail term="Cleared" mono>{money(dp.clearedAmount, dp.currency)}</Detail>
          <Detail term="Remaining" mono>{money(dp.remaining, dp.currency)}</Detail>
          <Detail term="Received" mono>{dp.receivedDate ?? "Not yet"}</Detail>
          <Detail term="Sales order" mono>{dp.salesOrderId ?? "None"}</Detail>
          <Detail term="Customer"><EntityName entity="customer" id={dp.customerId} /></Detail>
          <Detail term="Requested" mono>{dateTime(dp.createdAt)}</Detail>
        </dl>
      </Card>
      {done ? (
        <Alert tone="success" title="Down payment updated" live="status">
          {done}
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      {dp.status === "REQUESTED" ? (
        <Card title="Record receipt" headingLevel={3}>
          <form className={styles.form} onSubmit={receive} noValidate>
            <FormField id="dd-received" label="Received on" error={errors.receivedDate} hint={`Posts Dr bank / Cr ${account}.`}>
              {(c) => (
                <Input {...c} type="date" className={styles.mono} value={receivedDate} onChange={(e) => setReceivedDate(e.target.value)} invalid={Boolean(errors.receivedDate)} />
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "receive"}>
                Record as received
              </Button>
              <Button variant="danger" loading={busy === "cancel"} onClick={() => void run({ action: "cancel" })}>
                Cancel request
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {dp.status === "RECEIVED" ? (
        <Card title={dp.kind === "DOWN_PAYMENT" ? "Clear against the final invoice" : "Offset against an open item"} headingLevel={3}>
          <div className={styles.stack}>
            <form className={styles.form} onSubmit={clear} noValidate>
              <FormField id="dd-item" label="Open item" error={errors.openItemId} hint={dp.kind === "DOWN_PAYMENT" ? "The invoice of the same customer and currency." : "An invoice, fee or interest item of the same customer."}>
                {(c) => <EntityLookup entity="openItem" {...c} value={openItemId} onChange={(e) => setOpenItemId(e.target.value)} invalid={Boolean(errors.openItemId)} />}
              </FormField>
              <FormField id="dd-amount" label="Amount" optional error={errors.amount} hint="Empty clears the most possible: what remains, or the item's open amount if smaller.">
                {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={amount} onChange={(e) => setAmount(e.target.value)} invalid={Boolean(errors.amount)} />}
              </FormField>
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy === "clear"}>
                  {dp.kind === "DOWN_PAYMENT" ? "Clear" : "Offset"}
                </Button>
              </div>
            </form>
            {dp.kind === "SECURITY_DEPOSIT" ? (
              <div className={styles.actions}>
                <Button variant="secondary" loading={busy === "release"} onClick={() => void run({ action: "release" })}>
                  {`Release ${money(dp.remaining, dp.currency)} for refund`}
                </Button>
                <p className={styles.note}>Posts Dr 2195 / Cr bank for what remains.</p>
              </div>
            ) : null}
          </div>
        </Card>
      ) : null}
      <Card title="Clearings" headingLevel={3}>
        <DataTable
          label="Clearings"
          columns={clearingColumns}
          rows={dp.clearings ?? []}
          getRowKey={(row) => row.id}
          empty={{ title: "Nothing cleared yet", description: "Clearings against open items appear here." }}
        />
      </Card>
    </div>
  );
}
