import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import {
  describeProblem,
  getLoad,
  newKey,
  releaseLoad,
  startTender,
  TransportApiError,
  type LoadDetail,
  type LoadRelease,
  type LoadStatus,
  type NewTender,
  type Problem,
} from "./api/client";
import { CURRENCY, shortId, stateTone, statusLabel, UUID } from "./format";
import styles from "./pages.module.css";
import { RowsEditor, type RowField } from "./RowsEditor";
import { useLoad, type LoadState } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface TenderValues {
  loadId: string;
  carriers: Record<string, string>[];
  currency: string;
  timeoutMin: string;
}

const carrierFields: RowField[] = [
  { key: "carrierId", label: "Carrier id", mono: true, hint: "Rank order: the first carrier is asked first." },
  { key: "rateQuoted", label: "Rate quoted", decimal: true, hint: "Optional, such as the rate shop total." },
];

const blankCarrier = () => ({ carrierId: "", rateQuoted: "" });

/** Checks that mirror startTender in the service, and the request the form sends. */
export function validateTender(v: TenderValues): { errors: Record<string, string>; loadId?: string; input?: NewTender } {
  const errors: Record<string, string> = {};
  const loadId = v.loadId.trim();
  if (!UUID.test(loadId)) errors.loadId = "Enter the load id as a lower-case UUID.";
  const seen = new Set<string>();
  const carriers = v.carriers.map((c, i) => {
    const carrierId = c.carrierId.trim();
    const rate = (c.rateQuoted ?? "").trim();
    if (!UUID.test(carrierId)) errors[`carriers[${i}]`] = "Enter the carrier id as a lower-case UUID.";
    else if (seen.has(carrierId)) errors[`carriers[${i}]`] = "List each carrier once.";
    else if (rate !== "" && !/^\d{1,14}(\.\d{1,2})?$/.test(rate)) errors[`carriers[${i}]`] = "Enter the rate with at most two decimals, such as 412.50.";
    seen.add(carrierId);
    return rate === "" ? { carrierId } : { carrierId, rateQuoted: rate };
  });
  if (carriers.length === 0 || carriers.length > 20) errors.carriers = "Add between 1 and 20 carriers.";
  const rated = carriers.some((c) => c.rateQuoted !== undefined);
  if (rated && !CURRENCY.test(v.currency)) errors.currency = "Enter the currency of the quoted rates, such as USD.";
  const timeout = v.timeoutMin.trim() === "" ? 120 : Number(v.timeoutMin);
  if (!Number.isInteger(timeout) || timeout < 5 || timeout > 1440) errors.timeoutMin = "Enter whole minutes from 5 to 1440.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, loadId, input: { carriers, ...(rated ? { currency: v.currency } : {}), timeoutMin: timeout } };
}

const DUTY_TENDER = "transport.load.tender";

/** Load statuses a release takes; the service refuses others with TMS-0136. */
const RELEASABLE: LoadStatus[] = ["PLANNED", "RATED"];

/** The load's status and version as the planner last saw them; the release sends the version as If-Match. */
function LoadStatusLine({ loadId, state, seen }: { loadId: string; state: LoadState<LoadDetail | null>; seen: { status: LoadStatus; version: number } | null }) {
  if (loadId === "") return null;
  if (state.error) {
    return (
      <Alert tone="danger" title="Could not read the load" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  }
  if (!seen) return <p className={styles.note}>Reading the load status…</p>;
  return (
    <div>
      <p className={styles.flags}>
        Load status <Badge tone={stateTone(seen.status)}>{statusLabel(seen.status)}</Badge>
        <span className={styles.note}>version {seen.version}</span>
      </p>
      {RELEASABLE.includes(seen.status) ? null : (
        <p className={styles.note}>Only a planned or rated load is released to the tender workflow; a released load is tendered once.</p>
      )}
    </div>
  );
}

/**
 * Offer a load to carriers in route-guide order (FR-34.5): start the cascade here, or release the
 * load so the tender workflow runs it with its timers (#426).
 */
export function NewTenderPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const canRelease = holdsDuty(usePrincipalDuties(), DUTY_TENDER);
  const [values, setValues] = useState<TenderValues>({ loadId: params.get("loadId") ?? "", carriers: [blankCarrier()], currency: "USD", timeoutMin: "120" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<{ action: "start" | "release"; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<"start" | "release" | null>(null);
  const [released, setReleased] = useState<LoadRelease | null>(null);
  const key = useRef(newKey());

  // The release needs the load's version, so a planner who may release sees the load's status first.
  const lookup = canRelease && UUID.test(values.loadId.trim()) ? values.loadId.trim() : "";
  const loadState = useLoad((signal) => (lookup ? getLoad(lookup, signal) : Promise.resolve(null)), `tender-load:${lookup}`);
  const seen =
    released && released.loadId === lookup
      ? { status: released.loadStatus, version: released.loadVersion }
      : loadState.data && loadState.data.id === lookup
        ? { status: loadState.data.status, version: loadState.data.version }
        : null;

  function update(patch: Partial<TenderValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  function failed(action: "start" | "release", error: unknown) {
    const problem = describeProblem(error);
    if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
    else setFailure({ action, problem });
    if (problem.code === "idempotency_key_reuse") key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, loadId, input } = validateTender(values);
    setErrors(found);
    setFailure(null);
    if (!input || !loadId) return;
    setBusy("start");
    try {
      const tender = await startTender(loadId, input, key.current);
      navigate(`/transport/tenders/${tender.id}`);
    } catch (error) {
      failed("start", error);
    } finally {
      setBusy(null);
    }
  }

  async function release() {
    const { errors: found, loadId, input } = validateTender(values);
    setErrors(found);
    setFailure(null);
    if (!input || !loadId || !seen) return;
    setBusy("release");
    try {
      setReleased(await releaseLoad(loadId, input, seen.version, key.current));
    } catch (error) {
      failed("release", error);
      // A stale version (412) or a status the release does not take (409): show the load as it is now.
      if (error instanceof TransportApiError && (error.status === 412 || error.status === 409)) {
        setReleased(null);
        loadState.reload();
      }
    } finally {
      setBusy(null);
    }
  }

  const releasable = seen !== null && RELEASABLE.includes(seen.status);
  const justReleased = released !== null && released.loadId === lookup ? released : null;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Start tender"
        description="The load goes to the first carrier now. Each carrier has the response time you set before the load moves on to the next. Releasing the load instead hands the same cascade to the tender workflow."
      />
      <Alert tone="success" live="status">
        {justReleased
          ? `Load released for tendering (release ${shortId(justReleased.id)}). The load is now ${statusLabel(justReleased.loadStatus).toLowerCase()}; the tender workflow offers it to the first carrier and the tender appears on the board.`
          : null}
      </Alert>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="tender-load" label="Load id" hint="The load to tender (UUID)." error={errors.loadId}>
            {(control) => <Input {...control} className={styles.mono} value={values.loadId} onChange={(e) => update({ loadId: e.target.value })} invalid={Boolean(errors.loadId)} />}
          </FormField>
          <FormField id="tender-timeout" label="Response time (minutes)" hint="Per carrier; 5 to 1440, default 120." error={errors.timeoutMin}>
            {(control) => <Input {...control} inputMode="numeric" value={values.timeoutMin} onChange={(e) => update({ timeoutMin: e.target.value })} invalid={Boolean(errors.timeoutMin)} />}
          </FormField>
          <FormField id="tender-currency" label="Currency" hint="For the quoted rates." error={errors.currency}>
            {(control) => (
              <Input {...control} className={styles.mono} maxLength={3} value={values.currency} onChange={(e) => update({ currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />
            )}
          </FormField>
          <RowsEditor
            field="carriers"
            legend="Carriers in rank order"
            noun="carrier"
            fields={carrierFields}
            rows={values.carriers}
            errors={errors}
            blank={blankCarrier}
            onChange={(carriers) => update({ carriers })}
          />
          {canRelease ? (
            <div className={styles.formActions}>
              <LoadStatusLine loadId={lookup} state={loadState} seen={seen} />
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.action === "release" ? "Could not release the load" : "Could not start the tender"} live="alert">
                <ProblemMessage problem={failure.problem} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "start"}>
              Start tender
            </Button>
            {releasable ? (
              <Button loading={busy === "release"} onClick={release}>
                Release to the tender workflow
              </Button>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/transport/tenders">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
