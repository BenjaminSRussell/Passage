import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  checkLoadDG,
  describeProblem,
  getLoad,
  PARTY_ROLES,
  REGULATIONS,
  screenLoad,
  type DGCheck,
  type DGFinding,
  type DGLine,
  type DGRequest,
  type PaperLine,
  type PartyRole,
  type Problem,
  type Regulation,
  type ScreeningDecision,
  type ScreeningParty,
} from "./api/client";
import { Details } from "./Details";
import { AMOUNT, checkTone, dash, fixed, formatDateTime, stateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { RowsEditor, type RowField } from "./RowsEditor";
import { useLoad } from "./useLoad";

const findingColumns: DataTableColumn<DGFinding>[] = [
  { key: "severity", header: "Severity", render: (row) => <Badge tone={checkTone(row.severity)}>{statusLabel(row.severity)}</Badge> },
  { key: "ruleId", header: "Rule", mono: true },
  { key: "code", header: "Code", mono: true, render: (row) => row.code ?? dash },
  { key: "detail", header: "Detail" },
];

/** The DG lines as they print on the BOL (ADR 5.4.1, 49 CFR 172.202). */
const paperColumns: DataTableColumn<PaperLine>[] = [
  { key: "unNumber", header: "UN number", mono: true },
  { key: "properShippingName", header: "Proper shipping name" },
  { key: "class", header: "Class", mono: true },
  { key: "packingGroup", header: "Packing group", render: (row) => row.packingGroup || dash },
  { key: "quantity", header: "Quantity", align: "end", render: (row) => row.quantity },
  { key: "limitedQuantity", header: "Limited quantity", render: (row) => (row.limitedQuantity ? "Yes" : "No") },
  { key: "emergencyResponsePhone", header: "Emergency phone", mono: true, render: (row) => row.emergencyResponsePhone || dash },
];

const historyColumns: DataTableColumn<DGCheck>[] = [
  { key: "checkedAt", header: "Checked", render: (row) => formatDateTime(row.checkedAt) },
  { key: "regulation", header: "Regulation" },
  { key: "result", header: "Result", render: (row) => <Badge tone={checkTone(row.result)}>{statusLabel(row.result)}</Badge> },
  { key: "adrPoints", header: "ADR points", align: "end", render: (row) => fixed(row.adrPoints, 0) },
  { key: "findings", header: "Findings", align: "end", render: (row) => String(row.findings.length) },
  { key: "checkedBy", header: "Checked by" },
];

const dgFields: RowField[] = [
  { key: "unNumber", label: "UN number", mono: true, upper: true, hint: "Such as UN1203." },
  { key: "properShippingName", label: "Proper shipping name" },
  { key: "class", label: "Class", mono: true, upper: true, hint: "Such as 3, 2.1 or 1.1D." },
  { key: "packingGroup", label: "Packing group", options: ["", "I", "II", "III"] },
  { key: "quantity", label: "Quantity", decimal: true, hint: "Litres or kg." },
  { key: "transportCategory", label: "Transport category", options: ["0", "1", "2", "3", "4"] },
  { key: "limitedQuantity", label: "Limited quantity", options: ["No", "Yes"] },
];

const blankDGLine = () => ({ unNumber: "", properShippingName: "", class: "", packingGroup: "", quantity: "", transportCategory: "3", limitedQuantity: "No" });

const partyFields: RowField[] = [
  { key: "role", label: "Role", options: PARTY_ROLES },
  { key: "name", label: "Name" },
  { key: "country", label: "Country", upper: true, hint: "ISO code, such as DE." },
];

const blankParty = () => ({ role: "SHIP_TO", name: "", country: "" });

export interface DGValues {
  regulation: Regulation;
  emergencyResponsePhone: string;
  lines: Record<string, string>[];
}

/** Checks that mirror ValidateDGRequest in the service, and the request the form sends. */
export function validateDG(v: DGValues): { errors: Record<string, string>; input?: DGRequest } {
  const errors: Record<string, string> = {};
  const phone = v.emergencyResponsePhone.replace(/\s/g, "");
  if (phone !== "" && !/^\+[1-9]\d{6,14}$/.test(phone)) errors.emergencyResponsePhone = "Enter an E.164 number such as +18005551234.";
  const lines: DGLine[] = v.lines.map((l, i) => {
    const row = `lines[${i}]`;
    const un = l.unNumber.replace(/\s/g, "").toUpperCase();
    const cls = l.class.trim().toUpperCase();
    if (!/^UN\d{4}$/.test(un)) errors[row] = "Enter the UN number as UN followed by four digits.";
    else if (l.properShippingName.trim() === "") errors[row] = "Enter the proper shipping name.";
    else if (!/^[1-9](\.[1-6])?$|^1(\.[1-6])?[A-HJ-LNS]$/.test(cls)) errors[row] = "Enter the hazard class or division, such as 3, 2.1 or 1.1D.";
    else if (!AMOUNT.test(l.quantity.trim()) || /^0+(\.0+)?$/.test(l.quantity.trim())) errors[row] = "Enter the quantity, greater than zero.";
    return {
      unNumber: un,
      properShippingName: l.properShippingName.trim(),
      class: cls,
      packingGroup: l.packingGroup as DGLine["packingGroup"],
      quantity: l.quantity.trim(),
      transportCategory: Number(l.transportCategory),
      limitedQuantity: l.limitedQuantity === "Yes",
    };
  });
  if (lines.length === 0 || lines.length > 100) errors.lines = "Add between 1 and 100 dangerous-goods lines.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { regulation: v.regulation, ...(phone ? { emergencyResponsePhone: phone } : {}), lines } };
}

/** Run the transport DG check (FR-34.15, BR-34.15). A FAIL blocks tendering the load (AC-34.13). */
function DGCheckCard({ loadId, onChecked }: { loadId: string; onChecked(): void }) {
  const [values, setValues] = useState<DGValues>({ regulation: "ADR", emergencyResponsePhone: "", lines: [blankDGLine()] });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateDG(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await checkLoadDG(loadId, input);
      onChecked();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Check dangerous goods" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="dg-regulation" label="Regulation" hint="ADR for EU road, 49 CFR for US road.">
          {(control) => (
            <Select {...control} value={values.regulation} onChange={(e) => setValues((v) => ({ ...v, regulation: e.target.value as Regulation }))}>
              {REGULATIONS.map((r) => (
                <option key={r} value={r}>
                  {r === "49CFR" ? "49 CFR" : r}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="dg-phone" label="24-hour emergency phone" hint="Required before a regulated DG shipment." error={errors.emergencyResponsePhone}>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              inputMode="tel"
              value={values.emergencyResponsePhone}
              onChange={(e) => setValues((v) => ({ ...v, emergencyResponsePhone: e.target.value }))}
              invalid={Boolean(errors.emergencyResponsePhone)}
            />
          )}
        </FormField>
        <RowsEditor field="lines" legend="Dangerous-goods lines" noun="line" fields={dgFields} rows={values.lines} errors={errors} blank={blankDGLine} onChange={(lines) => setValues((v) => ({ ...v, lines }))} />
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not check the load" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Run DG check
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Screen the load's parties for denied parties and sanctions (FR-34.19, BR-34.17). */
function ScreeningCard({ loadId }: { loadId: string }) {
  const [parties, setParties] = useState<Record<string, string>[]>([blankParty()]);
  const [crossBorder, setCrossBorder] = useState("auto");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ScreeningDecision | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const input: ScreeningParty[] = parties.map((p, i) => {
      const country = p.country.trim().toUpperCase();
      if (p.name.trim() === "") found[`parties[${i}]`] = "Enter the party name.";
      else if (country !== "" && !/^[A-Z]{2}$/.test(country)) found[`parties[${i}]`] = "Enter the country as a two-letter ISO code.";
      return { role: p.role as PartyRole, name: p.name.trim(), ...(country ? { country } : {}) };
    });
    if (input.length === 0 || input.length > 20) found.parties = "Add between 1 and 20 parties.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await screenLoad(loadId, { crossBorder: crossBorder === "yes", parties: input }));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Screen parties" headingLevel={3}>
      <div className={styles.stack}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="screen-cross-border" label="Cross-border" hint="Without a screening provider, a cross-border load is not screened and warns (TMS-0134).">
            {(control) => (
              <Select {...control} value={crossBorder} onChange={(e) => setCrossBorder(e.target.value)}>
                <option value="auto">Decide from the party countries</option>
                <option value="yes">Yes, an export or import</option>
              </Select>
            )}
          </FormField>
          <RowsEditor field="parties" legend="Parties on the shipment" noun="party" fields={partyFields} rows={parties} errors={errors} blank={blankParty} onChange={setParties} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not screen the load" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Screen parties
            </Button>
          </div>
        </form>
        {result ? (
          <div className={styles.stack}>
            {result.decision === "HOLD" ? (
              <Alert tone="danger" title="Load held for trade compliance (TMS-0133)">
                A party needs review. The hold is not cleared automatically; a trade compliance user releases it with a reason.
              </Alert>
            ) : result.decision === "WARN" ? (
              <Alert tone="warning" title="Not screened (TMS-0134)">
                No screening provider is configured, so this cross-border load was not screened.
              </Alert>
            ) : (
              <Alert tone="success" title="Every party is clear" />
            )}
            <DataTable
              label="Screening results"
              columns={[
                { key: "role", header: "Role", render: (row) => statusLabel(row.role) },
                { key: "name", header: "Name" },
                { key: "status", header: "Result", render: (row) => <Badge tone={checkTone(row.status)}>{statusLabel(row.status)}</Badge> },
                { key: "code", header: "Code", mono: true, render: (row) => row.code ?? dash },
              ]}
              rows={result.parties}
              getRowKey={(row, i) => `${row.role}-${i}`}
            />
          </div>
        ) : null}
      </div>
    </Card>
  );
}

/** One load: its shipments and capacity, DG checks with shipping papers, and party screening. */
export function LoadDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getLoad(id, signal), `load:${id}`);
  const load = state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Load" />
        <Alert tone="danger" title="Could not load the load" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/transport/loads">Back to loads</Link>
      </div>
    );
  }
  if (!load) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Load" />
        <Card>
          <p className={styles.note}>Loading the load…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const latest = load.dgChecks[0];
  const blocked = latest?.result === "FAIL";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Load ${load.id.slice(0, 8)}`}
        meta={
          <span className={styles.flags}>
            <Badge tone={stateTone(load.status)}>{statusLabel(load.status)}</Badge>
            {latest ? <Badge tone={checkTone(latest.result)}>{`DG ${statusLabel(latest.result).toLowerCase()}`}</Badge> : null}
          </span>
        }
        actions={
          blocked ? null : (
            <Link className={buttonClassName("primary")} to={`/transport/tenders/new?loadId=${load.id}`}>
              Start tender
            </Link>
          )
        }
      />
      {blocked ? (
        <Alert tone="danger" title="Tendering is blocked (TMS-0130)">
          The latest dangerous-goods check failed. Split the load or correct its DG data and check it again before tendering.
        </Alert>
      ) : null}
      <Card>
        <Details
          entries={[
            { term: "Load", value: load.id, mono: true },
            { term: "Mode", value: load.mode },
            { term: "Ship-from site", value: load.shipFromSiteId, mono: true },
            { term: "Ship-to region", value: load.shipToRegion, mono: true },
            { term: "Pickup", value: load.pickupDate },
            { term: "Weight", value: `${fixed(load.totalWeightKg, 0)} of ${fixed(load.capacity.maxWeightKg, 0)} kg` },
            { term: "Volume", value: `${fixed(load.totalVolumeM3, 2)} of ${fixed(load.capacity.maxVolumeM3, 2)} m3` },
            { term: "Pallets", value: `${load.pallets} of ${load.capacity.maxPallets}` },
            { term: "Built", value: formatDateTime(load.createdAt) },
          ]}
        />
      </Card>
      <Card title={`Shipments (${load.shipmentIds.length})`} headingLevel={3}>
        <ul className={styles.trace}>
          {load.shipmentIds.map((s) => (
            <li key={s} className={styles.mono}>
              {s}
            </li>
          ))}
        </ul>
      </Card>
      {latest ? (
        <Card title={`Latest DG check: ${statusLabel(latest.result)}`} headingLevel={3}>
          <div className={styles.stack}>
            <p className={styles.note}>
              {latest.regulation === "ADR" ? `ADR 1.1.3.6 points ${fixed(latest.adrPoints, 0)}. ` : ""}Checked {formatDateTime(latest.checkedAt)} by {latest.checkedBy}.
            </p>
            {latest.findings.length > 0 ? (
              <DataTable label="DG findings" columns={findingColumns} rows={latest.findings} getRowKey={(row, i) => `${row.ruleId}-${i}`} />
            ) : (
              <p className={styles.note}>No rule fired.</p>
            )}
            <DataTable label="DG shipping papers" columns={paperColumns} rows={latest.shippingPapers} getRowKey={(row, i) => `${row.unNumber}-${i}`} />
          </div>
        </Card>
      ) : (
        <Card title="Dangerous goods" headingLevel={3}>
          <EmptyState compact headingLevel={3} title="No DG check yet" description="Run a check if the load carries dangerous goods. A load without dangerous goods needs none." />
        </Card>
      )}
      <DGCheckCard loadId={load.id} onChecked={state.reload} />
      {load.dgChecks.length > 1 ? (
        <Card title="DG check history" headingLevel={3}>
          <DataTable label="DG check history" columns={historyColumns} rows={load.dgChecks} getRowKey={(row) => row.id} />
        </Card>
      ) : null}
      <ScreeningCard loadId={load.id} />
      <div className={styles.actions}>
        <Link to={`/transport/tracking/${load.id}`}>Load tracking</Link>
        <Link to="/transport/loads">Back to loads</Link>
      </div>
    </div>
  );
}
