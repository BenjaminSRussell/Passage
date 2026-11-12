import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Badge, Button, Card, FormField, Input, type DataTableColumn, ListPage, loadedRows } from "../../shell/ui";
import { describeProblem, DUTY_COGS_POLICY, listLegalEntitySettings, saveLegalEntitySetting, type LegalEntitySetting, type Problem } from "./api/client";
import { formatDateTime, UUID, dash } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { PlainName } from "../sales/OrderNumber";

const columns: DataTableColumn<LegalEntitySetting>[] = [
  { key: "legalEntityId", header: "Legal entity", render: (row) => <PlainName entity="legalEntity" id={row.legalEntityId} /> },
  {
    key: "cogsAtShipment",
    header: "Cost of goods sold",
    render: (row) => <Badge tone={row.cogsAtShipment ? "info" : "neutral"}>{row.cogsAtShipment ? "At shipment (Dr 5000)" : "Delivered not invoiced (Dr 1405)"}</Badge>,
  },
  { key: "asnEnabled", header: "Advance ship notice", render: (row) => <Badge tone={row.asnEnabled ? "info" : "neutral"}>{row.asnEnabled ? "Sent with each post" : "Not sent"}</Badge> },
  {
    key: "gs1CompanyPrefix",
    header: "GS1 prefix (extension digit)",
    mono: true,
    render: (row) => `${row.gs1CompanyPrefix ?? "Process default"} (${row.ssccExtensionDigit ?? "default"})`,
  },
  { key: "packagingFeeBillable", header: "Packaging fee", render: (row) => <Badge tone={row.packagingFeeBillable ? "info" : "neutral"}>{row.packagingFeeBillable ? "Billed to the customer" : "Statistical only"}</Badge> },
  {
    key: "rtpTrackingEnabled",
    header: "Returnable packaging",
    render: (row) => <Badge tone={row.rtpTrackingEnabled ? "info" : "neutral"}>{row.rtpTrackingEnabled ? `Tracked, charge after ${row.rtpChargeDays} days` : "Not tracked"}</Badge>,
  },
  { key: "updatedAt", header: "Changed", render: (row) => (row.updatedAt ? formatDateTime(row.updatedAt) : dash) },
];

/**
 * Posting settings per legal entity (section 5.28.13): the COGS posting policy every packing slip follows, the
 * ASN default of a post, the GS1 company prefix and extension digit of the entity's SSCCs, whether its packaging
 * fees are billed and how its returnable packaging is tracked (on or off, and the charge days). Changing the COGS policy needs the ledger setup duty; the service enforces it.
 */
export function LegalEntitySettingsPage() {
  const canChangeCogs = holdsDuty(usePrincipalDuties(), DUTY_COGS_POLICY);
  const state = useLoad((signal) => listLegalEntitySettings(signal), "legal-entity-settings");
  const [legalEntityId, setLegalEntityId] = useState("");
  const [cogsAtShipment, setCogsAtShipment] = useState(false);
  const [asnEnabled, setAsnEnabled] = useState(false);
  const [gs1Prefix, setGs1Prefix] = useState("");
  const [extension, setExtension] = useState("");
  const [billable, setBillable] = useState(false);
  const [rtpTracking, setRtpTracking] = useState(true);
  const [rtpDays, setRtpDays] = useState("90");
  const [rtpDaysError, setRtpDaysError] = useState<string | undefined>();
  const [version, setVersion] = useState(0);
  const [error, setError] = useState<string | undefined>();
  const [prefixError, setPrefixError] = useState<string | undefined>();
  const [digitError, setDigitError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function open(row: LegalEntitySetting) {
    setLegalEntityId(row.legalEntityId);
    setCogsAtShipment(row.cogsAtShipment);
    setAsnEnabled(row.asnEnabled);
    setGs1Prefix(row.gs1CompanyPrefix ?? "");
    setExtension(row.ssccExtensionDigit === null ? "" : String(row.ssccExtensionDigit));
    setBillable(row.packagingFeeBillable);
    setRtpTracking(row.rtpTrackingEnabled);
    setRtpDays(String(row.rtpChargeDays));
    setRtpDaysError(undefined);
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
    const prefix = gs1Prefix.trim();
    const digit = extension.trim();
    const prefixBad = prefix !== "" && !/^[0-9]{6,12}$/.test(prefix);
    const digitBad = digit !== "" && !/^[0-9]$/.test(digit);
    setPrefixError(prefixBad ? "Enter 6 to 12 digits, or leave it empty for the process default." : undefined);
    setDigitError(digitBad ? "Enter a single digit from 0 to 9, or leave it empty." : undefined);
    const days = rtpDays.trim();
    const daysBad = !/^\d{2,3}$/.test(days) || Number(days) < 30 || Number(days) > 365;
    setRtpDaysError(daysBad ? "Enter a whole number of days from 30 to 365." : undefined);
    if (bad || prefixBad || digitBad || daysBad) return;
    setBusy(true);
    try {
      const known = state.data?.find((l) => l.legalEntityId === legalEntityId.trim());
      const saved = await saveLegalEntitySetting(legalEntityId.trim(), known ? known.version : version,
        { cogsAtShipment, asnEnabled, gs1CompanyPrefix: prefix === "" ? null : prefix, ssccExtensionDigit: digit === "" ? null : Number(digit), packagingFeeBillable: billable, rtpTrackingEnabled: rtpTracking, rtpChargeDays: Number(days) },
        key.current(),
      );
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
      <ListPage
        id="fulfillment-legal-entity-settings"
        title="Posting settings"
        description="Per legal entity: the cost of goods sold policy every packing slip follows (at shipment, or delivered not invoiced until the invoice), whether a post sends the advance ship notice, the GS1 company prefix its containers' SSCCs are issued under, whether billing charges its packaging fees, and how its returnable packaging is tracked and when a held balance becomes chargeable. A poster cannot change the policy; the ASN is a default the poster can turn off."
        noun="posting settings"
        columns={columns}
        useRows={() => loadedRows(state)}
        getRowKey={(row) => row.legalEntityId}
        onRowOpen={open}
        empty={{ title: "No legal entities yet", description: "A legal entity appears here once an order is released under it. Until then you can enter its id below." }}
      />
      <Card title="Change the posting settings">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="le-id" label="Legal entity" error={error}>
            {(control) => <EntityLookup entity="legalEntity" {...control} value={legalEntityId} invalid={Boolean(error)} onChange={(event) => { setLegalEntityId(event.target.value); setVersion(0); key.renew(); }} />}
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
          <FormField id="le-gs1" label="GS1 company prefix" error={prefixError} hint="6 to 12 digits. The SSCCs of this legal entity's containers are issued under it from the next container on. Empty takes the process default.">
            {(control) => <Input {...control} value={gs1Prefix} invalid={Boolean(prefixError)} inputMode="numeric" onChange={(event) => { setGs1Prefix(event.target.value); key.renew(); }} />}
          </FormField>
          <FormField id="le-ext" label="SSCC extension digit" error={digitError} hint="0 to 9. Empty takes the process default.">
            {(control) => <Input {...control} value={extension} invalid={Boolean(digitError)} inputMode="numeric" onChange={(event) => { setExtension(event.target.value); key.renew(); }} />}
          </FormField>
          <FormField id="le-fee" label="Bill the packaging fee" hint="Packing slips posted from now on tell billing to charge their packaging fee lines. Off keeps the fee statistical.">
            {(control) => <Input {...control} type="checkbox" checked={billable} onChange={(event) => { setBillable(event.target.checked); key.renew(); }} />}
          </FormField>
          <FormField id="le-rtp" label="Track returnable packaging" hint="On: a packing slip adds its returnable pallets and crates to the customer's balance. Off: slips posted from now on add nothing; a slip already posted still cancels the way it posted.">
            {(control) => <Input {...control} type="checkbox" checked={rtpTracking} onChange={(event) => { setRtpTracking(event.target.checked); key.renew(); }} />}
          </FormField>
          <FormField id="le-rtp-days" label="Returnable charge after days" error={rtpDaysError} hint="30 to 365. A balance held longer than this is due to be charged (rtp_charge_days).">
            {(control) => <Input {...control} value={rtpDays} invalid={Boolean(rtpDaysError)} inputMode="numeric" onChange={(event) => { setRtpDays(event.target.value); key.renew(); }} />}
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
