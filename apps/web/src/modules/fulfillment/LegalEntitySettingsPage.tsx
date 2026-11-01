import { useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, DUTY_COGS_POLICY, listLegalEntitySettings, saveLegalEntitySetting, type LegalEntitySetting, type Problem } from "./api/client";
import { formatDateTime, UUID, dash } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<LegalEntitySetting>[] = [
  { key: "legalEntityId", header: "Legal entity", mono: true },
  {
    key: "cogsAtShipment",
    header: "Cost of goods sold",
    render: (row) => <Badge tone={row.cogsAtShipment ? "info" : "neutral"}>{row.cogsAtShipment ? "At shipment (Dr 5000)" : "Delivered not invoiced (Dr 1405)"}</Badge>,
  },
  { key: "asnEnabled", header: "Advance ship notice", render: (row) => <Badge tone={row.asnEnabled ? "info" : "neutral"}>{row.asnEnabled ? "Sent with each post" : "Not sent"}</Badge> },
  { key: "updatedAt", header: "Changed", render: (row) => (row.updatedAt ? formatDateTime(row.updatedAt) : dash) },
];

/**
 * Posting settings per legal entity (section 5.28.13): the COGS posting policy every packing slip follows and the
 * ASN default of a post. Changing the COGS policy needs the ledger setup duty; the service enforces it.
 */
export function LegalEntitySettingsPage() {
  const canChangeCogs = holdsDuty(usePrincipalDuties(), DUTY_COGS_POLICY);
  const state = useLoad((signal) => listLegalEntitySettings(signal), "legal-entity-settings");
  const [legalEntityId, setLegalEntityId] = useState("");
  const [cogsAtShipment, setCogsAtShipment] = useState(false);
  const [asnEnabled, setAsnEnabled] = useState(false);
  const [version, setVersion] = useState(0);
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function open(row: LegalEntitySetting) {
    setLegalEntityId(row.legalEntityId);
    setCogsAtShipment(row.cogsAtShipment);
    setAsnEnabled(row.asnEnabled);
    setVersion(row.version);
    setError(undefined);
    setNotice(null);
    setFailure(null);
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    const bad = !UUID.test(legalEntityId.trim());
    setError(bad ? "Enter the legal entity id, or open a legal entity above." : undefined);
    if (bad) return;
    setBusy(true);
    try {
      const known = state.data?.find((l) => l.legalEntityId === legalEntityId.trim());
      const saved = await saveLegalEntitySetting(legalEntityId.trim(), known ? known.version : version, { cogsAtShipment, asnEnabled }, key.current());
      key.renew();
      setVersion(saved.version);
      setNotice("Saved. New packing slip posts use these settings.");
      state.reload();
    } catch (failed) {
      const problem = describeProblem(failed);
      setFailure(
        problem.code === "conflict_version"
          ? { ...problem, message: "Someone else changed this setting. Reload the list and open the legal entity again." }
          : problem.code === "FUL-0058"
            ? { ...problem, message: "This legal entity is outside your legal-entity scope." }
            : problem.code === "AUTH-0003"
              ? { ...problem, message: "Changing the cost of goods sold policy needs the ledger setup duty. Keep it as it is or ask finance." }
              : problem,
      );
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Posting settings"
        description="Per legal entity: the cost of goods sold policy every packing slip follows (at shipment, or delivered not invoiced until the invoice), and whether a post sends the advance ship notice. A poster cannot change the policy; the ASN is a default the poster can turn off."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the posting settings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Posting settings"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.legalEntityId}
          loading={state.loading}
          onRowOpen={open}
          empty={{ title: "No legal entities yet", description: "A legal entity appears here once an order is released under it. Until then you can enter its id below." }}
        />
      )}
      <Card title="Change the posting settings">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="le-id" label="Legal entity id" error={error}>
            {(control) => <Input {...control} value={legalEntityId} invalid={Boolean(error)} onChange={(event) => { setLegalEntityId(event.target.value); setVersion(0); key.renew(); }} />}
          </FormField>
          <FormField
            id="le-cogs"
            label="Post cost of goods sold at shipment"
            hint={
              canChangeCogs
                ? "Off: Dr 1405 delivered-not-invoiced. On: Dr 5000 COGS, so the slip never appears in the delivered-not-invoiced report. Applies to every packing slip of the legal entity."
                : "Off: Dr 1405 delivered-not-invoiced. On: Dr 5000 COGS. Changing this accounting policy needs the ledger setup duty."
            }
          >
            {(control) => <Input {...control} type="checkbox" checked={cogsAtShipment} disabled={!canChangeCogs} onChange={(event) => { setCogsAtShipment(event.target.checked); key.renew(); }} />}
          </FormField>
          <FormField id="le-asn" label="Send an advance ship notice (ASN)" hint="Publishes the containers, SSCCs and quantities of each posted slip for EDI.">
            {(control) => <Input {...control} type="checkbox" checked={asnEnabled} onChange={(event) => { setAsnEnabled(event.target.checked); key.renew(); }} />}
          </FormField>
          <ProblemAlert title="Could not save the settings" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save settings
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </Card>
    </div>
  );
}
