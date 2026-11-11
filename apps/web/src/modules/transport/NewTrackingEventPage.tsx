import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { describeProblem, recordTrackingEvent, TRACKING_CODES, TRACKING_SOURCES, type NewTrackingEvent, type Problem, type TrackingCode, type TrackingSource } from "./api/client";
import { trackingCodeLabels, UUID } from "./format";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface TrackingValues {
  loadId: string;
  stopNo: string;
  code: TrackingCode;
  occurredAt: string;
  location: string;
  source: TrackingSource;
  ref: string;
}

/** Checks that mirror ingestTracking in the service. occurredAt is a datetime-local value read as UTC. */
export function validateTracking(v: TrackingValues): { errors: Record<string, string>; input?: NewTrackingEvent } {
  const errors: Record<string, string> = {};
  const loadId = v.loadId.trim();
  if (!UUID.test(loadId)) errors.loadId = "Choose the load.";
  const stopNo = Number(v.stopNo);
  if (!Number.isInteger(stopNo) || stopNo < 1 || stopNo > 999) errors.stopNo = "Enter a stop number from 1 to 999.";
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(v.occurredAt)) errors.occurredAt = "Enter when it happened, in UTC.";
  const ref = v.ref.trim();
  if (ref === "" || ref.length > 64) errors.ref = "Enter the sender's reference, up to 64 characters.";
  if (v.location.trim().length > 100) errors.location = "Use at most 100 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const occurredAt = `${v.occurredAt.length === 16 ? `${v.occurredAt}:00` : v.occurredAt}Z`;
  const location = v.location.trim();
  return { errors, input: { loadId, stopNo, code: v.code, occurredAt, source: v.source, ref, ...(location ? { location } : {}) } };
}

/** Record a status report by hand, for example a driver's call (FR-34.6). */
export function NewTrackingEventPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<TrackingValues>({
    loadId: params.get("loadId") ?? "",
    stopNo: "1",
    code: "AF",
    occurredAt: "",
    location: "",
    source: "DRIVER",
    ref: "",
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<TrackingValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTracking(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await recordTrackingEvent(input);
      navigate(`/transport/tracking/${input.loadId}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Record status" description="Source and reference identify the report: sending the same one twice records it once." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="track-load" label="Load" entity="load" value={values.loadId} error={errors.loadId} onChange={(loadId) => update({ loadId })} />
          <FormField id="track-stop" label="Stop number" error={errors.stopNo}>
            {(control) => <Input {...control} inputMode="numeric" value={values.stopNo} onChange={(e) => update({ stopNo: e.target.value })} invalid={Boolean(errors.stopNo)} />}
          </FormField>
          <FormField id="track-code" label="Status" error={errors.code}>
            {(control) => (
              <Select {...control} value={values.code} onChange={(e) => update({ code: e.target.value as TrackingCode })}>
                {TRACKING_CODES.map((c) => (
                  <option key={c} value={c}>
                    {trackingCodeLabels[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="track-occurred" label="Occurred at (UTC)" error={errors.occurredAt}>
            {(control) => (
              <Input {...control} type="datetime-local" value={values.occurredAt} onChange={(e) => update({ occurredAt: e.target.value })} invalid={Boolean(errors.occurredAt)} />
            )}
          </FormField>
          <FormField id="track-location" label="Location" optional error={errors.location}>
            {(control) => <Input {...control} value={values.location} onChange={(e) => update({ location: e.target.value })} invalid={Boolean(errors.location)} />}
          </FormField>
          <FormField id="track-source" label="Source" error={errors.source}>
            {(control) => (
              <Select {...control} value={values.source} onChange={(e) => update({ source: e.target.value as TrackingSource })}>
                {TRACKING_SOURCES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="track-ref" label="Reference" hint="The sender's message or call reference." error={errors.ref}>
            {(control) => <Input {...control} className={styles.mono} value={values.ref} onChange={(e) => update({ ref: e.target.value })} invalid={Boolean(errors.ref)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not record the status" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record status
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/tracking">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
