import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  closeReservation,
  consumeReservation,
  createReservation,
  describeError,
  getReservation,
  listReservations,
  type ConsumeOutcome,
  type Consumption,
  type RegisterLine,
  type ReleaseOutcome,
  type Reservation,
  type ReservationLine,
  type ReservationStatus,
  type ReservationType,
} from "./api/client";
import {
  canClose,
  canConsume,
  DOCUMENT_TYPES,
  formatAmount,
  isMoney,
  RESERVATION_TYPES,
  reservationStatusLabel,
  reservationTypeLabel,
  toUnits,
  UUID,
  validateReservation,
  type DocumentType,
  type ReservationForm,
} from "./logic";
import styles from "./pages.module.css";
import { dash, Detail, formatDateTime, LoadError, ResultBadge } from "./parts";
import { useLoad } from "./useLoad";

const money = (key: string, header: string) => ({ key, header, align: "end" as const, mono: true });

const listColumns: DataTableColumn<Reservation>[] = [
  { key: "number", header: "Document", mono: true },
  { key: "type", header: "Type", render: (r) => reservationTypeLabel(r.type) },
  { key: "description", header: "Description", render: (r) => r.description || dash },
  { ...money("total", "Amount"), render: (r) => formatAmount(r.total) },
  { ...money("consumed", "Consumed"), render: (r) => formatAmount(r.consumed) },
  { ...money("open", "Remaining"), render: (r) => formatAmount(r.open) },
  { key: "expiryDate", header: "Expires", mono: true, render: (r) => r.expiryDate ?? dash },
  { key: "status", header: "Status", render: (r) => <StatusPill status={reservationStatusLabel(r.status)} /> },
];

const STATUSES: ReservationStatus[] = ["Open", "PartiallyConsumed", "Consumed", "Closed", "Expired"];

/** Earmarked funds list (FR-38.20, PRD 7.38.14): reservations, precommitments, blocks and revenue forecasts. */
export function ReservationsPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const rule = UUID.test(params.get("rule") ?? "") ? (params.get("rule") as string) : "";
  const [type, setType] = useState<ReservationType | "">("");
  const [status, setStatus] = useState<ReservationStatus | "">("");
  const state = useLoad((signal) => listReservations({ rule, type, status }, signal), `${rule}|${type}|${status}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Earmarked funds"
        description="Reservation documents hold funds on a control combination without a requisition. Purchase orders and other documents consume them later; close or expiry releases what is left."
        actions={
          <Link className={buttonClassName("primary")} to={`/budgeting/reservations/new${rule ? `?rule=${rule}` : ""}`}>
            New reservation
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="reservations-type" label="Type">
          {(c) => (
            <Select {...c} value={type} onChange={(e) => setType(e.target.value as ReservationType | "")}>
              <option value="">All types</option>
              {RESERVATION_TYPES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="reservations-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as ReservationStatus | "")}>
              <option value="">All statuses</option>
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {reservationStatusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load earmarked funds" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Earmarked funds"
          columns={listColumns}
          rows={state.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading && !state.data}
          empty={{ title: "No reservation documents", description: "Create a funds reservation to hold budget for planned spending." }}
          onRowOpen={(r) => navigate(`/budgeting/reservations/${r.id}`)}
        />
      )}
    </div>
  );
}

const emptyLine = (): RegisterLine & { key: string } => ({ key: crypto.randomUUID(), dimensionKey: "", spanKey: "", amount: "" });

/** Creates an earmarked funds document. Fund-holding types are checked against available funds first. */
export function CreateReservationPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [form, setForm] = useState<ReservationForm>({ ruleVersionId: params.get("rule") ?? "", type: "FundsReservation", description: "", expiryDate: "" });
  const [lines, setLines] = useState([emptyLine()]);
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  function update(index: number, patch: Partial<RegisterLine>) {
    setLines((current) => current.map((l, i) => (i === index ? { ...l, ...patch } : l)));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, ruleVersionId: form.ruleVersionId.trim(), description: form.description.trim(), expiryDate: form.expiryDate.trim() };
    const cleanLines = lines.map(({ dimensionKey, spanKey, amount }) => ({ dimensionKey: dimensionKey.trim(), spanKey: spanKey.trim(), amount: amount.trim() }));
    const found = validateReservation(clean, cleanLines);
    setErrors(found);
    setFailure(null);
    if (found.length > 0) return;
    const input = { ...clean, expiryDate: clean.expiryDate || undefined, lines: cleanLines };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const created = await createReservation(input, key.current.value);
      navigate(`/budgeting/reservations/${created.id}`);
    } catch (err) {
      setFailure(describeError(err, "The reservation could not be created."));
    } finally {
      setBusy(false);
    }
  }

  const typeHint = RESERVATION_TYPES.find((t) => t.value === form.type)?.hint;
  const blocking = form.type === "FundsBlocking";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New reservation"
        description="The document is checked against available funds like a purchase order. A line over the block threshold refuses the whole document."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="reservation-rule" label="Rule version" hint="The budget control rule version whose balances hold the funds.">
            {(c) => <Input {...c} className={styles.mono} value={form.ruleVersionId} onChange={(e) => setForm((f) => ({ ...f, ruleVersionId: e.target.value }))} />}
          </FormField>
          <FormField id="reservation-type" label="Type" hint={typeHint}>
            {(c) => (
              <Select
                {...c}
                value={form.type}
                onChange={(e) => {
                  const next = e.target.value as ReservationType;
                  setForm((f) => ({ ...f, type: next, expiryDate: next === "FundsBlocking" ? "" : f.expiryDate }));
                }}
              >
                {RESERVATION_TYPES.map((t) => (
                  <option key={t.value} value={t.value}>
                    {t.label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="reservation-description" label="Description" optional>
            {(c) => <Textarea {...c} rows={2} value={form.description} onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))} />}
          </FormField>
          <FormField
            id="reservation-expiry"
            label="Expiry date"
            optional
            hint={blocking ? "Funds blocking does not expire." : "The first day the document no longer holds funds; the residual is released then."}
          >
            {(c) => <Input {...c} type="date" disabled={blocking} value={form.expiryDate} onChange={(e) => setForm((f) => ({ ...f, expiryDate: e.target.value }))} />}
          </FormField>
          {lines.map((line, i) => (
            <fieldset key={line.key} className={styles.lineSet}>
              <legend className={styles.legend}>Line {i + 1}</legend>
              <FormField id={`reservation-dim-${i}`} label="Dimension">
                {(c) => <Input {...c} className={styles.mono} value={line.dimensionKey} onChange={(e) => update(i, { dimensionKey: e.target.value })} />}
              </FormField>
              <FormField id={`reservation-span-${i}`} label="Time span" hint="For example FY2026 or 2026-09.">
                {(c) => <Input {...c} className={styles.mono} value={line.spanKey} onChange={(e) => update(i, { spanKey: e.target.value })} />}
              </FormField>
              <FormField id={`reservation-amount-${i}`} label="Amount">
                {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={line.amount} onChange={(e) => update(i, { amount: e.target.value })} />}
              </FormField>
              {lines.length > 1 ? (
                <Button variant="ghost" onClick={() => setLines((current) => current.filter((_, k) => k !== i))}>
                  Remove line {i + 1}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button onClick={() => setLines((current) => [...current, emptyLine()])}>Add line</Button>
            <Button type="submit" variant="primary" loading={busy}>
              Create reservation
            </Button>
            <Link className={buttonClassName("ghost")} to="/budgeting/reservations">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <Alert tone="danger" live="alert">
        {errors.length > 0 ? (
          <ul className={styles.errorList}>
            {errors.map((msg) => (
              <li key={msg}>{msg}</li>
            ))}
          </ul>
        ) : (
          failure
        )}
      </Alert>
    </div>
  );
}

const lineColumns: DataTableColumn<ReservationLine>[] = [
  { key: "dimensionKey", header: "Dimension", mono: true },
  { key: "spanKey", header: "Time span", mono: true },
  { ...money("amount", "Amount"), render: (l) => formatAmount(l.amount) },
  { ...money("consumed", "Consumed"), render: (l) => formatAmount(l.consumed) },
  { ...money("released", "Released"), render: (l) => formatAmount(l.released) },
  { ...money("open", "Remaining"), render: (l) => formatAmount(l.open) },
];

const consumptionColumns: DataTableColumn<Consumption>[] = [
  { key: "consumedAt", header: "When", render: (c) => formatDateTime(c.consumedAt) },
  { key: "documentType", header: "Document type" },
  { key: "documentId", header: "Document", mono: true },
  { ...money("amount", "Amount"), render: (c) => formatAmount(c.amount) },
  { key: "encumbranceId", header: "Encumbrance", render: (c) => <Link to={`/budgeting/encumbrances/${c.encumbranceId}`}>Open encumbrance</Link> },
];

interface ConsumeForm {
  lineId: string;
  documentType: DocumentType;
  documentId: string;
  documentLineId: string;
  amount: string;
}

/** One reservation document: lines, consumption history, consume and close (FR-38.20, BR-38.14). */
export function ReservationDetailPage() {
  const { id = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getReservation(id, signal), `${id}|${reload}`);
  const [form, setForm] = useState<ConsumeForm>({ lineId: "", documentType: "PurchaseOrder", documentId: "", documentLineId: "", amount: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof ConsumeForm, string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [consumed, setConsumed] = useState<ConsumeOutcome | null>(null);
  const [released, setReleased] = useState<ReleaseOutcome | null>(null);
  const [busy, setBusy] = useState<"consume" | "close" | null>(null);
  const consumeKey = useRef({ body: "", value: crypto.randomUUID() });
  const closeKey = useRef(crypto.randomUUID());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Reservation" />
        <LoadError title="Could not load this reservation" error={state.error} onRetry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Reservation" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const res = state.data;
  const openLines = res.lines.filter((l) => toUnits(l.open) > 0n);
  const lineId = form.lineId || openLines[0]?.lineId || "";
  const consumable = canConsume(res.type, res.status) && openLines.length > 0;

  async function consume(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, lineId, documentId: form.documentId.trim(), documentLineId: form.documentLineId.trim(), amount: form.amount.trim() };
    const found: typeof errors = {};
    if (!UUID.test(clean.documentId)) found.documentId = "Enter the follow-on document id (a UUID).";
    if (!UUID.test(clean.documentLineId)) found.documentLineId = "Enter the document line id (a UUID).";
    const line = res.lines.find((l) => l.lineId === clean.lineId);
    if (!isMoney(clean.amount) || toUnits(clean.amount) <= 0n) found.amount = "Enter a positive decimal with at most 4 places.";
    else if (line && toUnits(clean.amount) > toUnits(line.open)) found.amount = `At most ${formatAmount(line.open)} remains on this line.`;
    setErrors(found);
    setFailure(null);
    setReleased(null);
    if (Object.keys(found).length > 0) return;
    const body = JSON.stringify(clean);
    if (consumeKey.current.body !== body) consumeKey.current = { body, value: crypto.randomUUID() };
    setBusy("consume");
    try {
      setConsumed(await consumeReservation(id, clean, consumeKey.current.value));
      consumeKey.current = { body: "", value: crypto.randomUUID() };
      setForm((f) => ({ ...f, documentId: "", documentLineId: "", amount: "" }));
      setReload((n) => n + 1);
    } catch (err) {
      setFailure(describeError(err, "The reservation could not be consumed."));
    } finally {
      setBusy(null);
    }
  }

  async function close() {
    setFailure(null);
    setConsumed(null);
    setBusy("close");
    try {
      setReleased(await closeReservation(id, closeKey.current));
      setReload((n) => n + 1);
    } catch (err) {
      setFailure(describeError(err, "The reservation could not be closed."));
    } finally {
      setBusy(null);
    }
  }

  const field = (name: "documentId" | "documentLineId" | "amount", label: string) => (
    <FormField id={`consume-${name}`} label={label} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          className={styles.mono}
          inputMode={name === "amount" ? "decimal" : undefined}
          value={form[name]}
          invalid={Boolean(errors[name])}
          onChange={(e) => setForm((f) => ({ ...f, [name]: e.target.value }))}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${reservationTypeLabel(res.type)} ${res.number}`}
        description={res.description || "Earmarked funds on one or more control combinations."}
        meta={<StatusPill status={reservationStatusLabel(res.status)} />}
        actions={
          <>
            <Link to={`/budgeting/rules/${res.ruleVersionId}`}>Open the rule</Link>
            {canClose(res.type, res.status) ? (
              <Button onClick={close} loading={busy === "close"}>
                Close and release the remainder
              </Button>
            ) : null}
          </>
        }
      />
      <StatGroup label="Reservation summary">
        <StatTile label="Amount" value={formatAmount(res.total)} />
        <StatTile label="Consumed" value={formatAmount(res.consumed)} />
        <StatTile label="Released" value={formatAmount(res.released)} />
        <StatTile label="Remaining" value={formatAmount(res.open)} />
      </StatGroup>
      <Card>
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {res.legalEntityId}
          </Detail>
          <Detail term="Expires" mono>
            {res.expiryDate ?? "No expiry"}
          </Detail>
          <Detail term="Created">{formatDateTime(res.createdAt)}</Detail>
          <Detail term="Closed">{formatDateTime(res.closedAt)}</Detail>
          <Detail term="Creation check">{res.check ? <ResultBadge result={res.check.result} /> : res.checkId ? <Link to={`/budgeting/checks/${res.checkId}`}>Open the check</Link> : "Not checked (holds no funds)"}</Detail>
        </dl>
      </Card>
      <DataTable label="Reservation lines" columns={lineColumns} rows={res.lines} getRowKey={(l) => l.lineId} />
      <h3 className={styles.sectionTitle}>Consumption</h3>
      <DataTable
        label="Consumption history"
        columns={consumptionColumns}
        rows={res.consumptions}
        getRowKey={(c) => c.id}
        empty={{ title: "Nothing consumed yet", description: "Documents that draw on this reservation appear here with the encumbrance they created." }}
      />
      {consumable ? (
        <Card title="Consume by a follow-on document" headingLevel={3}>
          <form className={styles.form} onSubmit={consume} noValidate>
            <FormField id="consume-line" label="Reservation line">
              {(c) => (
                <Select {...c} value={lineId} onChange={(e) => setForm((f) => ({ ...f, lineId: e.target.value }))}>
                  {openLines.map((l) => (
                    <option key={l.lineId} value={l.lineId}>
                      {`${l.dimensionKey} · ${l.spanKey} (${formatAmount(l.open)} remaining)`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="consume-documentType" label="Document type">
              {(c) => (
                <Select {...c} value={form.documentType} onChange={(e) => setForm((f) => ({ ...f, documentType: e.target.value as DocumentType }))}>
                  {DOCUMENT_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {field("documentId", "Document id")}
            {field("documentLineId", "Document line id")}
            {field("amount", "Amount")}
            <p className={styles.note}>The amount moves from the reservation to an encumbrance of the document line, so available funds do not change.</p>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "consume"}>
                Consume
              </Button>
            </div>
          </form>
        </Card>
      ) : res.type === "FundsBlocking" ? (
        <p className={styles.note}>Funds blocking documents cannot be consumed. Close the block to release the funds.</p>
      ) : null}
      <Alert tone="danger" live="alert">
        {failure}
      </Alert>
      <Alert tone="success" live="status">
        {consumed
          ? `Consumed ${formatAmount(consumed.consumed)}; ${formatAmount(consumed.open)} remains on the reservation.`
          : released
            ? `Closed ${released.number}; ${formatAmount(released.released)} released to available funds.`
            : null}
      </Alert>
    </div>
  );
}
