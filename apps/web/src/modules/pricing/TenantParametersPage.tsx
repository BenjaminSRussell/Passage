import { useEffect, useRef, useState } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  describeProblem,
  getPricingTenantParameters,
  newKey,
  PricingApiError,
  setPricingTenantParameters,
  type CampaignKillSwitchEffect,
  type PricingTenantParameters,
  type Problem,
} from "./api/client";
import { useLoad } from "./useLoad";
import styles from "./pages.module.css";

const DUTY_ADMIN = "pricing.admin";

const KILL_EFFECTS: { value: CampaignKillSwitchEffect; label: string }[] = [
  { value: "NEXT_RESOLVE", label: "Next resolve on the replica that took it" },
  { value: "IMMEDIATE_CACHE_FLUSH", label: "Flush every replica's cache at once" },
];

/** Pricing parameters of the tenant (PRD 5.26.13): the snapshot lifetime and the campaign kill switch effect. */
export function TenantParametersPage() {
  const canSave = holdsDuty(usePrincipalDuties(), DUTY_ADMIN);
  const load = useLoad((signal) => getPricingTenantParameters(signal), "tenant-parameters");
  const [saved, setSaved] = useState<PricingTenantParameters | null>(null);
  const current = saved ?? load.data;
  const [ttl, setTtl] = useState("");
  const [effect, setEffect] = useState<CampaignKillSwitchEffect>("NEXT_RESOLVE");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());
  const fieldError = (name: string): string | undefined => failure?.fields?.[name];

  useEffect(() => {
    if (!load.data) return;
    setTtl(load.data.cacheTtlSeconds ? String(load.data.cacheTtlSeconds) : "");
    setEffect(load.data.campaignKillSwitchEffect);
  }, [load.data]);

  /** A changed value is a new command, so it gets a new Idempotency-Key. */
  function edited() {
    setDone(false);
    key.current = newKey();
  }

  async function save() {
    if (!current) return;
    const text = ttl.trim();
    if (text !== "" && !/^\d+$/.test(text)) {
      setFailure({ code: "COM-0001", message: "The snapshot lifetime must be a whole number of seconds.", fields: { cacheTtlSeconds: "Enter whole seconds from 10 to 600, or leave it empty." } });
      return;
    }
    setBusy(true);
    setFailure(null);
    setDone(false);
    try {
      const next = await setPricingTenantParameters(
        { cacheTtlSeconds: text === "" ? 0 : Number(text), campaignKillSwitchEffect: effect, version: current.version },
        key.current,
      );
      setSaved(next);
      setDone(true);
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
      <PageHeader title="Tenant pricing parameters" description="Settings that apply to every legal entity of the tenant." />
      {load.error ? (
        <Alert tone="danger" title="Could not load the tenant parameters" live="alert">
          {load.error.message} ({load.error.code})
          <div className={styles.formActions}>
            <Button type="button" variant="secondary" onClick={load.reload}>
              Retry
            </Button>
          </div>
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not complete the request" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {load.loading && !current ? <Skeleton height="tile" /> : null}
      {current ? (
        <Card title="Record cache and campaign kill switch">
          <div className={styles.form}>
            <FormField id="tp-ttl" label="Snapshot lifetime (seconds)" error={fieldError("cacheTtlSeconds")} hint="From 10 to 600. Leave it empty to follow the deployment's setting.">
              {(c) => (
                <Input
                  {...c}
                  className={styles.mono}
                  inputMode="numeric"
                  value={ttl}
                  disabled={!canSave}
                  onChange={(e) => {
                    setTtl(e.target.value);
                    edited();
                  }}
                />
              )}
            </FormField>
            <FormField id="tp-kill-effect" label="Campaign kill switch effect" error={fieldError("campaignKillSwitchEffect")} hint="The replica that takes a kill or restore always drops its own snapshots. Flushing every replica empties the others as soon as they read the event; otherwise they follow within the snapshot lifetime.">
              {(c) => (
                <Select
                  {...c}
                  value={effect}
                  disabled={!canSave}
                  onChange={(e) => {
                    setEffect(e.target.value as CampaignKillSwitchEffect);
                    edited();
                  }}
                >
                  {KILL_EFFECTS.map((k) => (
                    <option key={k.value} value={k.value}>
                      {k.label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {done ? (
              <Alert tone="success" title="Parameters saved" live="status">
                Version {current.version} is in effect.
              </Alert>
            ) : null}
            <div className={styles.formActions}>
              {canSave ? (
                <Button type="button" variant="primary" loading={busy} onClick={save}>
                  Save tenant parameters
                </Button>
              ) : (
                <span className={styles.note}>Changing them needs the {DUTY_ADMIN} duty.</span>
              )}
              <span className={styles.note}>{current.version === 0 ? "Defaults, nothing saved yet." : `Version ${current.version}${current.updatedBy ? `, saved by ${current.updatedBy}` : ""}`}</span>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
