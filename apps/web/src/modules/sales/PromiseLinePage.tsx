import { useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  CHANGEABLE_LINE_STATUSES,
  describeProblem,
  getOrder,
  promiseLine,
  type AtpPeriod,
  type Order,
  type Problem,
  type PromiseLineInput,
  type PromiseLineResult,
  type Versioned,
} from "./api/client";
import { DECIMAL, sentence } from "./format";
import styles from "./pages.module.css";
import { ScheduleTable } from "./OrderSchedule";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface PromiseValues {
  lineNo: string;
  /** true: whole quantity on one date; false: partial deliveries allowed; null: follow the partial delivery policy of the product and customer. */
  completeOnly: boolean | null;
  periods: AtpPeriod[];
}

/** Checks that mirror the service (api/schedule.go). Keys are the service's field paths. */
export function validatePromise(values: PromiseValues): { errors: Errors; input?: PromiseLineInput; lineNo?: number } {
  const errors: Errors = {};
  if (!/^[1-9]\d*$/.test(values.lineNo)) errors.lineNo = "Choose the line to promise.";
  if (values.periods.length === 0) errors.periods = "Add at least one bucket.";
  values.periods.forEach((p, i) => {
    const at = (name: string) => `periods[${i}].${name}`;
    if (!DATE.test(p.date)) errors[at("date")] = "Choose the bucket date.";
    else if (i > 0 && DATE.test(values.periods[i - 1].date) && p.date <= values.periods[i - 1].date) errors[at("date")] = "Use a date later than the previous bucket.";
    if (!DECIMAL.test(p.supply)) errors[at("supply")] = "Enter 0 or more.";
    if (!DECIMAL.test(p.demand)) errors[at("demand")] = "Enter 0 or more.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, lineNo: Number(values.lineNo), input: { ...(values.completeOnly === null ? {} : { completeOnly: values.completeOnly }), periods: values.periods } };
}

const POLICY_SENTENCE: Record<string, string> = {
  COMPLETE_ONLY: "the complete only partial delivery policy",
  ONE_SHIPMENT: "the one shipment partial delivery policy",
  ALLOWED: "the partial delivery policy, which allows deliveries in parts",
};

/** Says where the whole-quantity choice came from when the service took it from the partial delivery policy. */
export function promisePolicyNote(result: PromiseLineResult): string {
  if (!result.partialDelivery) return "";
  const basis = POLICY_SENTENCE[result.partialDelivery] ?? "the partial delivery policy";
  return result.completeOnly ? `The whole quantity was kept on one date, following ${basis}.` : `Partial deliveries were allowed, following ${basis}.`;
}

/** The result in one sentence: how much ships by the requested date and how much is backordered. */
export function promiseOutcome(result: PromiseLineResult, lineNo: number): string {
  const lines = result.scheduleLines.filter((s) => s.lineNo === lineNo);
  const late = lines.filter((s) => s.status !== "CONFIRMED");
  if (late.length === 0) return `Line ${lineNo} can ship in full by the requested date.`;
  const onTime = lines.length > late.length;
  const undated = lines.filter((s) => s.status === "UNPROMISED");
  const dated = late.filter((s) => s.status === "BACKORDER");
  const parts: string[] = [];
  if (dated.length > 0) parts.push(`${dated.length} later ${dated.length === 1 ? "delivery" : "deliveries"}`);
  if (undated.length > 0) parts.push(`${undated.map((s) => s.quantity).join(" + ")} with no supply`);
  return onTime || lines.length > 1 ? `Line ${lineNo} was split: ${parts.join(" and ")} on backorder.` : `Line ${lineNo} is on backorder: ${parts.join(" and ")}.`;
}

/**
 * Promise one line of a confirmed order against ATP buckets (FR-27.6, BR-27.1):
 * the result is stored as schedule lines, and what ships after the requested
 * date appears on the backorder list. The buckets are entered until the stored
 * ATP projection exists.
 */
export function PromiseLinePage() {
  const { id = "" } = useParams();
  const [search] = useSearchParams();
  const state = useLoad((signal) => getOrder(id, signal), id);
  const [latest, setLatest] = useState<Versioned<Order> | null>(null);
  const [values, setValues] = useState<PromiseValues>({ lineNo: search.get("line") ?? "", completeOnly: null, periods: [{ date: "", supply: "0", demand: "0" }] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<{ value: PromiseLineResult; lineNo: number } | null>(null);
  const [busy, setBusy] = useState(false);
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Promise order line" />
        <Alert tone="danger" title="Could not load the order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Promise order line" />
        <span className="visually-hidden">Loading the order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: order, etag } = current;
  const promisable = order.lines.filter((l) => CHANGEABLE_LINE_STATUSES.includes(l.lineStatus) && !l.supplementaryOf);
  const stale = failure?.code === "SLS-0006";

  function updateBucket(i: number, patch: Partial<AtpPeriod>) {
    setValues((v) => ({ ...v, periods: v.periods.map((p, idx) => (idx === i ? { ...p, ...patch } : p)) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input, lineNo } = validatePromise(values);
    if (!found.lineNo && !promisable.some((l) => String(l.lineNo) === values.lineNo)) found.lineNo = "Choose the line to promise.";
    setErrors(found);
    setFailure(null);
    if (!input || lineNo === undefined || found.lineNo) return;
    setBusy(true);
    try {
      const res = await promiseLine(order.id, lineNo, etag, input);
      setLatest({ value: res.value.order, etag: res.etag });
      setResult({ value: res.value, lineNo });
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Promise order ${order.orderNumber}`}
        description="Place a line against supply and demand per bucket, earliest first. Quantity that cannot ship by the requested date becomes backorder."
        actions={
          <Link className={buttonClassName("secondary")} to={`/sales/orders/${order.id}`}>
            Back to order
          </Link>
        }
      />
      {order.status !== "CONFIRMED" && order.status !== "PARTIALLY_SHIPPED" ? (
        <Alert tone="warning" title="Only a confirmed order can be promised">
          This order is {sentence(order.status)}. Confirm it first.
        </Alert>
      ) : order.orderType === "JOURNAL" ? (
        <Alert tone="info" title="A journal order is not promised">
          Journal orders never reserve or consume ATP.
        </Alert>
      ) : promisable.length === 0 ? (
        <Alert tone="info" title="No line can be promised">
          Every line is past reservation, cancelled or a supplementary line that follows its parent.
        </Alert>
      ) : (
        <Card>
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Promise a line">
            <FormField id="promise-line" label="Line" error={errors.lineNo}>
              {(control) => (
                <Select {...control} value={values.lineNo} onChange={(e) => setValues((v) => ({ ...v, lineNo: e.target.value }))} invalid={Boolean(errors.lineNo)}>
                  <option value="">Choose a line</option>
                  {promisable.map((l) => (
                    <option key={l.id} value={String(l.lineNo)}>
                      {`Line ${l.lineNo}: ${l.quantity} ${l.unit} requested ${l.requestedShipDate}`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField
              id="promise-complete"
              label="Delivery in parts"
              hint="Follow the policy takes the delivery tolerance rule of the product and customer, else the legal entity's partial delivery default. Complete only and one shipment keep the whole quantity on one date."
            >
              {(control) => (
                <Select
                  {...control}
                  value={values.completeOnly === null ? "policy" : values.completeOnly ? "whole" : "parts"}
                  onChange={(e) => setValues((v) => ({ ...v, completeOnly: e.target.value === "policy" ? null : e.target.value === "whole" }))}
                >
                  <option value="policy">Follow the partial delivery policy</option>
                  <option value="whole">Whole quantity on one date</option>
                  <option value="parts">Allow partial deliveries</option>
                </Select>
              )}
            </FormField>
            <div className={`${styles.formActions} ${styles.lines}`}>
              {values.periods.map((p, i) => (
                <fieldset key={i} className={styles.lineCard}>
                  <legend className={styles.lineLegend}>Bucket {i + 1}</legend>
                  <FormField id={`promise-${i}-date`} label="Date" error={errors[`periods[${i}].date`]}>
                    {(control) => <Input {...control} type="date" value={p.date} onChange={(e) => updateBucket(i, { date: e.target.value })} invalid={Boolean(errors[`periods[${i}].date`])} />}
                  </FormField>
                  <FormField id={`promise-${i}-supply`} label="Supply" error={errors[`periods[${i}].supply`]}>
                    {(control) => <Input {...control} inputMode="decimal" value={p.supply} onChange={(e) => updateBucket(i, { supply: e.target.value })} invalid={Boolean(errors[`periods[${i}].supply`])} />}
                  </FormField>
                  <FormField id={`promise-${i}-demand`} label="Demand" error={errors[`periods[${i}].demand`]}>
                    {(control) => <Input {...control} inputMode="decimal" value={p.demand} onChange={(e) => updateBucket(i, { demand: e.target.value })} invalid={Boolean(errors[`periods[${i}].demand`])} />}
                  </FormField>
                  {values.periods.length > 1 ? (
                    <div className={styles.formActions}>
                      <Button size="sm" variant="ghost" onClick={() => setValues((v) => ({ ...v, periods: v.periods.filter((_, idx) => idx !== i) }))}>
                        Remove bucket {i + 1}
                      </Button>
                    </div>
                  ) : null}
                </fieldset>
              ))}
              {errors.periods ? (
                <p role="alert" className={styles.fieldError}>
                  {errors.periods}
                </p>
              ) : null}
              <div>
                <Button onClick={() => setValues((v) => ({ ...v, periods: [...v.periods, { date: "", supply: "0", demand: "0" }] }))}>Add bucket</Button>
              </div>
            </div>
            {failure ? (
              <div className={styles.formActions}>
                <Alert
                  tone="danger"
                  title={stale ? "Someone else changed this order" : "Could not promise the line"}
                  live="alert"
                  action={
                    stale ? (
                      <Button
                        size="sm"
                        onClick={() => {
                          setLatest(null);
                          setFailure(null);
                          state.reload();
                        }}
                      >
                        Reload order
                      </Button>
                    ) : undefined
                  }
                >
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Promise line
              </Button>
            </div>
          </form>
        </Card>
      )}
      <p role="status" className={styles.muted}>
        {result ? [promiseOutcome(result.value, result.lineNo), promisePolicyNote(result.value)].filter(Boolean).join(" ") : ""}
      </p>
      {result ? <ScheduleTable lines={result.value.scheduleLines} /> : null}
    </div>
  );
}
