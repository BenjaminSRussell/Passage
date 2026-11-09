import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Alert, Button, Card, PageHeader, Input, Skeleton } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { fetchLedger, patchLedgerReinstatement, type Ledger } from "./api/client";
import { LedgerPicker, useLedgerParam } from "./LedgerPicker";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/** The duty PATCH /ledgers/{id} enforces; the service has the last word. */
export const DUTY_LEDGER_SETTINGS = "ledger.ledger.write";

function SettingsCard({ ledger }: { ledger: Ledger }) {
  const queryClient = useQueryClient();
  const canEdit = holdsDuty(usePrincipalDuties(), DUTY_LEDGER_SETTINGS);
  const [allow, setAllow] = useState(ledger.allowReinstate ?? true);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const changed = allow !== (ledger.allowReinstate ?? true);

  async function save() {
    setBusy(true);
    setFailure(null);
    setSaved(false);
    try {
      const updated = await patchLedgerReinstatement(ledger.id, allow, ledger.version);
      // The PATCH answer is the new ledger: it carries the next version for the next If-Match.
      queryClient.setQueryData(["ledger", "ledger", ledger.id], updated);
      setSaved(true);
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <div className={styles.form}>
        <p className={styles.note}>
          {ledger.code} · {ledger.name} · {ledger.currency} · chart {ledger.chartCode}
        </p>
        <div className={styles.formActions}>
          <span className={styles.check}>
            <Input id="ls-reinstate" type="checkbox" checked={allow} disabled={!canEdit} onChange={(e) => { setAllow(e.target.checked); setSaved(false); }} />
            <label htmlFor="ls-reinstate">Allow reinstatement</label>
          </span>
          <span className={styles.note}>
            When on, reversing a reversing entry posts the original again. When off, such a reversal is refused. Entries already reversed are not changed.
          </span>
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the ledger settings" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        {saved ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Ledger settings saved" live="status">
              Reinstatement is now {allow ? "allowed" : "refused"} on {ledger.code}.
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          {canEdit ? (
            <Button variant="primary" loading={busy} disabled={!changed} onClick={() => void save()}>
              Save ledger settings
            </Button>
          ) : (
            <span className={styles.note}>You can view these settings; changing them needs the ledger write duty.</span>
          )}
        </div>
      </div>
    </Card>
  );
}

/** Ledger settings that govern reversals (FR-11.8). */
export function LedgerSettingsPage() {
  const [ledgerId, setLedgerId] = useLedgerParam();
  const result = useQuery({ queryKey: ["ledger", "ledger", ledgerId], queryFn: () => fetchLedger(ledgerId), enabled: ledgerId !== "" });

  return (
    <div className={styles.stack}>
      <PageHeader title="Ledger settings" description="Settings of one ledger that change how entries are reversed." />
      <LedgerPicker id="ls-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={setLedgerId} />
      {ledgerId === "" ? (
        <p className={styles.note}>Enter a ledger id to see its settings.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the ledger" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the ledger…</span>
            <Skeleton height="control" />
          </div>
        </Card>
      ) : (
        <SettingsCard key={result.data.id} ledger={result.data} />
      )}
    </div>
  );
}
