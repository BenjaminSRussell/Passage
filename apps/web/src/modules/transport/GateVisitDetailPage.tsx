import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { checkOut, describeProblem, getGateVisit, newKey, recordSeal, type GateLogEntry, type Problem } from "./api/client";
import { Details } from "./Details";
import { AMOUNT, arrivalText, dash, fixed, formatDateTime, gateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const detailText = (detail: Record<string, string>) =>
  Object.entries(detail)
    .map(([k, v]) => `${k} ${v}`)
    .join(", ") || dash;

const logColumns: DataTableColumn<GateLogEntry>[] = [
  { key: "occurredAt", header: "When", render: (row) => formatDateTime(row.occurredAt) },
  { key: "kind", header: "Event", render: (row) => <Badge tone={gateTone(row.kind)}>{statusLabel(row.kind)}</Badge> },
  { key: "detail", header: "Detail", mono: true, render: (row) => detailText(row.detail) },
  { key: "recordedBy", header: "Recorded by" },
];

/** A gate visit: arrival against the appointment, seal, check-out with seal verification and detention, and its gate log (FR-34.14). */
export function GateVisitDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getGateVisit(id, signal), `gate-visit:${id}`);
  const visit = state.data;
  const [seal, setSeal] = useState("");
  const [declared, setDeclared] = useState("");
  const [rate, setRate] = useState("");
  const [checkOutAt, setCheckOutAt] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState<"seal" | "out" | null>(null);
  const key = useRef(newKey());

  async function submitSeal(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setDone(null);
    const value = seal.trim();
    if (value === "" || value.length > 20) {
      setErrors({ seal: "Enter the seal number, up to 20 characters." });
      return;
    }
    setErrors({});
    setBusy("seal");
    try {
      await recordSeal(id, value);
      setSeal("");
      setDone(`Seal ${value} recorded.`);
      state.reload();
    } catch (error) {
      setFailure({ title: "Could not record the seal", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  async function submitCheckOut(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setDone(null);
    const found: Record<string, string> = {};
    const r = rate.trim();
    if (r !== "" && !AMOUNT.test(r)) found.rate = "Enter the hourly rate as a decimal, such as 85.00.";
    let at: string | undefined;
    if (checkOutAt !== "") {
      const d = new Date(checkOutAt);
      if (Number.isNaN(d.getTime())) found.checkOutAt = "Enter the check-out date and time.";
      else at = d.toISOString();
    }
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy("out");
    try {
      const out = await checkOut({ visitId: id, sealNo: declared.trim(), ...(at ? { checkOutAt: at } : {}), ...(r ? { hourlyRate: r } : {}) }, key.current);
      setDone(out.detention && out.detention.hours > 0 ? `Checked out. Detention ${out.detention.hours} h (TMS-0126).` : "Checked out.");
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure({ title: problem.code === "TMS-0129" ? "Check-out blocked" : "Could not check out", problem });
      key.current = newKey();
      // A blocked attempt is written to the gate log, so show it.
      if (problem.code === "TMS-0129") state.reload();
    } finally {
      setBusy(null);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Gate visit" />
        <Alert tone="danger" title="Could not load the gate visit" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link to="/transport/gate">Back to the gate log</Link>
      </div>
    );
  }
  if (!visit) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Gate visit" />
        <Card>
          <p className={styles.note}>Loading the gate visit…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const inYard = visit.status === "IN_YARD";
  const needsSeal = inYard && visit.direction === "OUTBOUND" && !visit.sealNo;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Trailer ${visit.trailerNo}`}
        description={`${visit.carrierScac}, ${statusLabel(visit.direction).toLowerCase()}`}
        meta={<Badge tone={gateTone(visit.status)}>{statusLabel(visit.status)}</Badge>}
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          <ProblemMessage problem={failure.problem} />
        </Alert>
      ) : null}
      <Card>
        <Details
          entries={[
            { term: "Visit", value: visit.id, mono: true },
            { term: "Site", value: visit.siteId, mono: true },
            { term: "Load", value: visit.loadId || dash, mono: true },
            { term: "Driver", value: `${visit.driverName} (${visit.driverLicenseId})` },
            { term: "Appointment", value: visit.walkIn ? "Walk-in" : formatDateTime(visit.scheduledAt) },
            {
              term: "Arrival",
              value: visit.arrival ? (
                <span className={styles.flags}>
                  <Badge tone={gateTone(visit.arrival.status)}>{statusLabel(visit.arrival.status)}</Badge>
                  {arrivalText(visit.arrival.status, visit.arrival.deltaMin)}
                  {visit.arrival.code ? ` (${visit.arrival.code})` : ""}
                </span>
              ) : (
                "Walk-in"
              ),
            },
            { term: "Checked in", value: formatDateTime(visit.checkedInAt) },
            { term: "Seal", value: visit.sealNo || "None recorded", mono: Boolean(visit.sealNo) },
            { term: "Free time", value: `${visit.freeTimeMin} min` },
            { term: "Checked out", value: formatDateTime(visit.checkedOutAt) },
            {
              term: "Detention",
              value: visit.detention
                ? visit.detention.hours > 0
                  ? `${visit.detention.detentionMin} min over free time, ${visit.detention.hours} h charged: ${fixed(visit.detention.charge, 2)}`
                  : `None (dwell ${visit.detention.dwellMin} min)`
                : dash,
            },
          ]}
        />
      </Card>
      {inYard ? (
        <Card title="Seal at load confirmation" headingLevel={3}>
          <div className={styles.stack}>
            <p className={styles.note}>
              {needsSeal
                ? "This outbound load has no seal yet. Record the seal once the load is confirmed; the driver cannot leave without it."
                : "Record a new seal if the trailer was resealed. The gate log keeps every seal."}
            </p>
            <form className={styles.inlineForm} onSubmit={submitSeal} noValidate>
              <FormField id="visit-seal" label="Seal number" error={errors.seal}>
                {(control) => <Input {...control} className={styles.mono} maxLength={20} value={seal} onChange={(e) => setSeal(e.target.value)} invalid={Boolean(errors.seal)} />}
              </FormField>
              <Button type="submit" loading={busy === "seal"}>
                Record seal
              </Button>
            </form>
          </div>
        </Card>
      ) : null}
      {inYard ? (
        <Card title="Check out" headingLevel={3}>
          <div className={styles.stack}>
            <p className={styles.note}>The seal the driver declares must match the recorded seal; a mismatch blocks the check-out and is logged.</p>
            <form className={styles.inlineForm} onSubmit={submitCheckOut} noValidate>
              <FormField id="checkout-seal" label="Declared seal" hint={visit.sealNo ? undefined : "Leave empty for an unsealed trailer."} error={errors.sealNo}>
                {(control) => <Input {...control} className={styles.mono} maxLength={20} value={declared} onChange={(e) => setDeclared(e.target.value)} invalid={Boolean(errors.sealNo)} />}
              </FormField>
              <FormField id="checkout-rate" label="Detention rate per hour" hint="The carrier's DETENTION accessorial." optional error={errors.rate ?? errors.hourlyRate}>
                {(control) => <Input {...control} inputMode="decimal" value={rate} onChange={(e) => setRate(e.target.value)} invalid={Boolean(errors.rate ?? errors.hourlyRate)} />}
              </FormField>
              <FormField id="checkout-at" label="Checked out" hint="Leave empty for now." optional error={errors.checkOutAt}>
                {(control) => <Input {...control} type="datetime-local" value={checkOutAt} onChange={(e) => setCheckOutAt(e.target.value)} invalid={Boolean(errors.checkOutAt)} />}
              </FormField>
              <Button type="submit" variant="primary" loading={busy === "out"}>
                Check out
              </Button>
            </form>
          </div>
        </Card>
      ) : null}
      <Card title="Gate log" headingLevel={3}>
        <DataTable label="Gate log" columns={logColumns} rows={visit.log} getRowKey={(row) => row.id} empty={{ title: "No gate log rows" }} />
      </Card>
      <Link to="/transport/gate">Back to the gate log</Link>
    </div>
  );
}

