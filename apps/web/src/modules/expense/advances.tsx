import { useRef, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  decideAdvance,
  describeProblem,
  getAdvance,
  listAdvances,
  newIdempotencyKey,
  payAdvance,
  requestAdvance,
  returnAdvance,
  type AdvanceStatus,
  type CashAdvance,
  type Decision,
  type NewAdvanceInput,
} from "./api/client";
import { CURRENCY, DAY, DECIMAL, splitProblem } from "./format";
import { amount, useActionKeys, usePaged, useRecord, UUID, type Failure } from "./persisted";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export const ADVANCE_STATUS: Record<AdvanceStatus, { label: string; tone: BadgeTone }> = {
  DRAFT: { label: "Draft", tone: "neutral" },
  SUBMITTED: { label: "Awaiting approval", tone: "info" },
  APPROVED: { label: "Approved", tone: "info" },
  REJECTED: { label: "Rejected", tone: "danger" },
  PAID: { label: "Paid", tone: "warning" },
  PARTIALLY_SETTLED: { label: "Partially settled", tone: "warning" },
  SETTLED: { label: "Settled", tone: "success" },
};

function AdvanceBadge({ status }: { status: AdvanceStatus }) {
  const s = ADVANCE_STATUS[status] ?? { label: status, tone: "neutral" as BadgeTone };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

const columns: DataTableColumn<CashAdvance>[] = [
  { key: "purpose", header: "Purpose" },
  { key: "status", header: "Status", render: (a) => <AdvanceBadge status={a.status} /> },
  { key: "tripEnd", header: "Trip end", mono: true },
  {
    key: "dueDate",
    header: "Settle by",
    mono: true,
    render: (a) => (
      <span className={styles.badges}>
        {a.dueDate}
        {a.overdue ? <Badge tone="danger">Overdue</Badge> : null}
      </span>
    ),
  },
  { key: "amount", header: "Amount", align: "end", mono: true, render: (a) => amount(a.currency, a.amount) },
  { key: "open", header: "Open", align: "end", mono: true, render: (a) => amount(a.currency, a.open) },
];

/** FR-43.15 cash advances visible to the caller, filtered by status. */
export function AdvancesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<AdvanceStatus | "">("");
  const list = usePaged((cursor) => listAdvances({ status: status || undefined, cursor }), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cash advances"
        description="Advances are approved, paid through accounts payable, and settled by expense reports or a cash return before their settle-by date."
        actions={
          <Button variant="primary" icon="receipt" onClick={() => navigate("/expense/cash-advances/new")}>
            Request advance
          </Button>
        }
      />
      <Card>
        <div className={styles.form}>
          <FormField id="advances-status" label="Status">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as AdvanceStatus | "")}>
                <option value="">All statuses</option>
                {(Object.keys(ADVANCE_STATUS) as AdvanceStatus[]).map((s) => (
                  <option key={s} value={s}>
                    {ADVANCE_STATUS[s].label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {list.failure ? (
        <Alert tone="danger" title="The cash advances could not be loaded" live="alert" action={<Button size="sm" onClick={list.retry}>Try again</Button>}>
          {list.failure.message} ({list.failure.code})
        </Alert>
      ) : null}
      <DataTable
        label="Cash advances"
        columns={columns}
        rows={list.items}
        loading={list.loading}
        getRowKey={(a) => a.id}
        empty={
          status
            ? { title: `No ${ADVANCE_STATUS[status].label.toLowerCase()} advances`, description: "Choose another status to see other advances." }
            : { title: "No cash advances yet", description: "Request an advance before a trip; it is paid once approved." }
        }
        onRowOpen={(a) => navigate(`/expense/cash-advances/${a.id}`)}
      />
      {list.next && !list.loading ? (
        <div className={styles.formActions}>
          <Button loading={list.loadingMore} onClick={list.more}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

export interface NewAdvanceValues {
  purpose: string;
  amount: string;
  currency: string;
  tripEnd: string;
  legalEntityId: string;
  workerId: string;
  travelRequisitionId: string;
}

export const emptyAdvance: NewAdvanceValues = { purpose: "", amount: "", currency: "USD", tripEnd: "", legalEntityId: "", workerId: "", travelRequisitionId: "" };

export function validateAdvance(v: NewAdvanceValues): { errors: Errors; input?: NewAdvanceInput } {
  const errors: Errors = {};
  const purpose = v.purpose.trim();
  const currency = v.currency.trim().toUpperCase();
  const amt = v.amount.trim();
  const requisition = v.travelRequisitionId.trim();
  if (purpose === "" || purpose.length > 500) errors.purpose = "Enter the purpose, up to 500 characters.";
  if (!DECIMAL.test(amt) || Number(amt) <= 0) errors.amount = "Enter an amount greater than zero, such as 500.00.";
  if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!DAY.test(v.tripEnd)) errors.tripEnd = "Enter the last travel day as YYYY-MM-DD.";
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.workerId.trim())) errors.workerId = "Enter the worker id (a UUID).";
  if (requisition !== "" && !UUID.test(requisition)) errors.travelRequisitionId = "Enter the requisition id (a UUID) or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      purpose,
      amount: amt,
      currency,
      tripEnd: v.tripEnd,
      legalEntityId: v.legalEntityId.trim(),
      workerId: v.workerId.trim(),
      ...(requisition ? { travelRequisitionId: requisition } : {}),
    },
  };
}

const ADVANCE_FIELDS = ["purpose", "amount", "currency", "tripEnd", "legalEntityId", "workerId", "travelRequisitionId"];

/** Request a cash advance; it goes straight to approval. */
export function NewAdvancePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<NewAdvanceValues>(emptyAdvance);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newIdempotencyKey());

  function update(patch: Partial<NewAdvanceValues>) {
    key.current = newIdempotencyKey();
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAdvance(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const adv = await requestAdvance(input, key.current);
      navigate(`/expense/cash-advances/${adv.id}`);
    } catch (err) {
      const split = splitProblem(describeProblem(err), ADVANCE_FIELDS);
      setErrors(split.fields);
      setFailure(split.failure);
      setBusy(false);
    }
  }

  const field = (name: keyof NewAdvanceValues, label: string, hint?: string, mono = false, optional = false) => (
    <FormField id={`advance-${name}`} label={label} hint={hint} optional={optional} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          className={mono ? styles.mono : undefined}
          type={name === "tripEnd" ? "date" : "text"}
          inputMode={name === "amount" ? "decimal" : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Request a cash advance" description="Up to 2,000.00 per request. Settle it within 30 days after the trip ends, or new advances are blocked." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>{field("purpose", "Purpose")}</div>
          {field("amount", "Amount", undefined, true)}
          {field("currency", "Currency", "ISO code such as USD or EUR.", true)}
          {field("tripEnd", "Trip end", "The last travel day.")}
          {field("legalEntityId", "Legal entity", "The legal entity id; it must be in your scope.", true)}
          {field("workerId", "Worker", "The worker id the advance is paid to.", true)}
          {field("travelRequisitionId", "Travel requisition", "The requisition this trip was approved under.", true, true)}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The advance was not requested" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Request advance
            </Button>
            <Button variant="ghost" onClick={() => navigate("/expense/cash-advances")}>
              Cancel
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One advance: its settlement amounts, the approval decision, the AP payment and cash returns. */
export function AdvanceDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { record: adv, setRecord, failure: loadFailure, reload } = useRecord(() => getAdvance(id), id);
  const keyFor = useActionKeys();
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<{ title: string; message: string } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [prepayment, setPrepayment] = useState("");
  const [returned, setReturned] = useState("");
  const [fieldError, setFieldError] = useState<Errors>({});

  async function act(action: string, title: string, run: (version: number) => Promise<CashAdvance>, done: string): Promise<boolean> {
    if (!adv) return false;
    setBusy(action);
    setFailure(null);
    setNotice(null);
    try {
      setRecord(await run(adv.version));
      setNotice(done);
      return true;
    } catch (err) {
      const p = describeProblem(err);
      setFailure({ title, message: `${p.message} (${p.code})` });
      if (p.code === "EXP-ADV-008") reload();
      return false;
    } finally {
      setBusy(null);
    }
  }

  function decide(decision: Decision) {
    if (!adv) return;
    void act(
      decision,
      "The decision was not recorded",
      (v) => decideAdvance(adv.id, decision, v, keyFor("decide", v, decision)),
      decision === "APPROVE" ? "Advance approved." : "Advance rejected.",
    );
  }

  function pay(event: FormEvent) {
    event.preventDefault();
    if (!adv) return;
    const prepaymentId = prepayment.trim();
    if (!UUID.test(prepaymentId)) {
      setFieldError({ prepaymentId: "Enter the AP prepayment id (a UUID)." });
      return;
    }
    setFieldError({});
    void act("pay", "The payment was not recorded", (v) => payAdvance(adv.id, prepaymentId, v, keyFor("pay", v, prepaymentId)), "Payment recorded.").then(
      (ok) => ok && setPrepayment(""),
    );
  }

  function recordReturn(event: FormEvent) {
    event.preventDefault();
    if (!adv) return;
    const value = (returned.trim() || adv.open).trim();
    if (!DECIMAL.test(value) || Number(value) <= 0) {
      setFieldError({ amount: "Enter the returned amount, greater than zero." });
      return;
    }
    setFieldError({});
    void act(
      "return",
      "The return was not recorded",
      (v) => returnAdvance(adv.id, value, v, keyFor("return", v, value)),
      `Return of ${amount(adv.currency, value)} recorded.`,
    ).then((ok) => ok && setReturned(""));
  }

  if (loadFailure) {
    if (loadFailure.code === "EXP-ADV-006" || loadFailure.code === "COM-0001") {
      return (
        <EmptyState
          icon="receipt"
          title="Cash advance not found"
          description="It does not exist or is outside your legal entities."
          action={<Button onClick={() => navigate("/expense/cash-advances")}>Back to cash advances</Button>}
        />
      );
    }
    return (
      <Alert tone="danger" title="The cash advance could not be loaded" live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
        {loadFailure.message} ({loadFailure.code})
      </Alert>
    );
  }

  if (!adv) {
    return (
      <div className={styles.stack} aria-busy="true" aria-label="Loading the cash advance">
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const open = adv.status === "PAID" || adv.status === "PARTIALLY_SETTLED";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={adv.purpose}
        description={`Advance of ${amount(adv.currency, adv.amount)} for worker ${adv.workerId}, settle by ${adv.dueDate}.`}
        meta={<AdvanceBadge status={adv.status} />}
        actions={
          <>
            <Button variant="ghost" onClick={() => navigate("/expense/cash-advances")}>
              All cash advances
            </Button>
            {adv.status === "SUBMITTED" ? (
              <>
                <Button variant="danger" loading={busy === "REJECT"} onClick={() => decide("REJECT")}>
                  Reject
                </Button>
                <Button variant="primary" loading={busy === "APPROVE"} onClick={() => decide("APPROVE")}>
                  Approve
                </Button>
              </>
            ) : null}
          </>
        }
      />
      <StatGroup label="Advance amounts">
        <StatTile label="Amount" value={amount(adv.currency, adv.amount)} />
        <StatTile label="Paid" value={amount(adv.currency, adv.paid)} />
        <StatTile label="Settled by reports" value={amount(adv.currency, adv.settled)} />
        <StatTile label="Returned" value={amount(adv.currency, adv.returned)} />
        <StatTile label="Open" value={amount(adv.currency, adv.open)} hint={`Settle by ${adv.dueDate}`} />
      </StatGroup>
      {notice ? <Alert tone="success" title={notice} live="status" /> : null}
      {adv.overdue ? (
        <Alert tone="warning" title="Past its settle-by date" live="off">
          {amount(adv.currency, adv.open)} is still open after {adv.dueDate}, so new advances for this worker are blocked until it is settled.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          {failure.message}
        </Alert>
      ) : null}
      <Card title="Details">
        <dl className={styles.facts}>
          <dt>Trip end</dt>
          <dd className={styles.mono}>{adv.tripEnd}</dd>
          <dt>Travel requisition</dt>
          <dd className={styles.mono}>{adv.travelRequisitionId ?? "None"}</dd>
          <dt>AP prepayment</dt>
          <dd className={styles.mono}>{adv.apPrepaymentId ?? "Not paid yet"}</dd>
          <dt>Requested</dt>
          <dd className={styles.mono}>{adv.createdAt.slice(0, 10)}</dd>
        </dl>
      </Card>
      {adv.status === "APPROVED" ? (
        <Card title="Record the AP payment">
          <form className={styles.form} onSubmit={pay} noValidate>
            <FormField id="advance-prepayment" label="AP prepayment" hint="The prepayment id from accounts payable that paid the worker." error={fieldError.prepaymentId}>
              {(c) => <Input {...c} className={styles.mono} value={prepayment} onChange={(e) => setPrepayment(e.target.value)} invalid={Boolean(fieldError.prepaymentId)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "pay"}>
                Record payment
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {open ? (
        <Card title="Record a cash return">
          <form className={styles.form} onSubmit={recordReturn} noValidate>
            <FormField id="advance-return" label="Returned amount" hint={`At most the open ${amount(adv.currency, adv.open)}. Leave empty to return all of it.`} error={fieldError.amount}>
              {(c) => (
                <Input {...c} className={styles.mono} inputMode="decimal" placeholder={adv.open} value={returned} onChange={(e) => setReturned(e.target.value)} invalid={Boolean(fieldError.amount)} />
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "return"}>
                Record return
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
