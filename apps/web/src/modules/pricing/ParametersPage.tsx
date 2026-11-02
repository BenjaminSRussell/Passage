import { useRef, useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, EmptyState, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { describeProblem, getPricingParameters, newKey, PricingApiError, setPricingParameters, type PricingParameters, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";

const DUTY_ADMIN = "pricing.admin";

/** Pricing parameters of one legal entity (PRD 5.26.13). Reading needs pricing.view; saving needs pricing.admin. */
export function ParametersPage() {
  const canSave = holdsDuty(usePrincipalDuties(), DUTY_ADMIN);
  const [legalEntityId, setLegalEntityId] = useState("");
  const [idError, setIdError] = useState("");
  const [loaded, setLoaded] = useState<PricingParameters | null>(null);
  const [fourEyes, setFourEyes] = useState(false);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const key = useRef(newKey());

  async function load(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    if (!UUID.test(legalEntityId.trim())) {
      setIdError("Enter the legal entity id (a UUID).");
      return;
    }
    setIdError("");
    setLoading(true);
    try {
      const params = await getPricingParameters(legalEntityId.trim());
      setLoaded(params);
      setFourEyes(params.journalFourEyes);
      key.current = newKey();
    } catch (error) {
      setLoaded(null);
      setFailure(describeProblem(error));
    } finally {
      setLoading(false);
    }
  }

  async function save() {
    if (!loaded) return;
    setBusy(true);
    setFailure(null);
    setSaved(false);
    try {
      const next = await setPricingParameters({ legalEntityId: loaded.legalEntityId, journalFourEyes: fourEyes, version: loaded.version }, key.current);
      setLoaded(next);
      setSaved(true);
      key.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (error instanceof PricingApiError && error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Pricing parameters" description="Settings that change how a legal entity prices and posts." />
      <Card>
        <form className={styles.form} onSubmit={load} noValidate>
          <FormField id="pp-legal-entity" label="Legal entity id" error={idError}>
            {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} invalid={Boolean(idError)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="secondary" loading={loading}>
              Load parameters
            </Button>
          </div>
        </form>
      </Card>
      {failure ? (
        <Alert tone="danger" title="Could not complete the request" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {loading ? <Skeleton height="tile" /> : null}
      {!loading && !loaded && !failure ? <EmptyState title="No legal entity loaded" description="Enter a legal entity id to read its parameters." /> : null}
      {!loading && loaded ? (
        <Card title="Price journals">
          <div className={styles.form}>
            <div className={styles.formActions}>
              <span className={styles.check}>
                <Input
                  id="pp-four-eyes"
                  type="checkbox"
                  checked={fourEyes}
                  disabled={!canSave}
                  onChange={(e) => {
                    setFourEyes(e.target.checked);
                    setSaved(false);
                    key.current = newKey();
                  }}
                />
                <label htmlFor="pp-four-eyes">Four eyes on journals</label>
              </span>
              <span className={styles.note}>The user who created a price journal cannot post it. Another pricing manager posts it.</span>
            </div>
            {saved ? (
              <Alert tone="success" title="Parameters saved" live="status">
                Version {loaded.version} is in effect.
              </Alert>
            ) : null}
            <div className={styles.formActions}>
              {canSave ? (
                <Button type="button" variant="primary" loading={busy} onClick={save}>
                  Save parameters
                </Button>
              ) : (
                <span className={styles.note}>Changing them needs the {DUTY_ADMIN} duty.</span>
              )}
              <span className={styles.note}>{loaded.version === 0 ? "Defaults, nothing saved yet." : `Version ${loaded.version}${loaded.updatedBy ? `, saved by ${loaded.updatedBy}` : ""}`}</span>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
