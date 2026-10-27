import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, rateShipment, type Problem, type Quote, type RateRequest, type RateResponse } from "./api/client";
import { AMOUNT, DATE, fixed, money, shortId, statusLabel, UUID } from "./format";
import styles from "./pages.module.css";

interface Values {
  shipmentId: string;
  originZone: string;
  destinationZone: string;
  weightKg: string;
  volumeM3: string;
  pallets: string;
  distanceKm: string;
  shipDate: string;
  accessorials: string;
}

type Errors = Partial<Record<keyof Values, string>>;

const initial: Values = {
  shipmentId: "",
  originZone: "",
  destinationZone: "",
  weightKg: "",
  volumeM3: "0",
  pallets: "0",
  distanceKm: "",
  shipDate: "",
  accessorials: "",
};

/** Checks that mirror the service, and the request the form sends. */
export function validateRate(v: Values): { errors: Errors; body?: RateRequest } {
  const errors: Errors = {};
  if (!UUID.test(v.shipmentId.trim())) errors.shipmentId = "Enter the shipment id as a lower-case UUID.";
  const zone = (s: string) => s.trim().length >= 1 && s.trim().length <= 20;
  if (!zone(v.originZone)) errors.originZone = "Enter the origin zone, up to 20 characters.";
  if (!zone(v.destinationZone)) errors.destinationZone = "Enter the destination zone, up to 20 characters.";
  if (!AMOUNT.test(v.weightKg.trim())) errors.weightKg = "Enter the weight in kg, such as 80.";
  if (!AMOUNT.test(v.volumeM3.trim())) errors.volumeM3 = "Enter the volume in m³, or 0.";
  if (!/^\d{1,4}$/.test(v.pallets.trim())) errors.pallets = "Enter a whole number of pallets.";
  if (v.distanceKm.trim() !== "" && !AMOUNT.test(v.distanceKm.trim())) errors.distanceKm = "Enter the distance in km, or leave it empty.";
  if (!DATE.test(v.shipDate)) errors.shipDate = "Enter the ship date.";
  const codes = v.accessorials
    .split(",")
    .map((c) => c.trim().toUpperCase())
    .filter((c) => c !== "");
  if (codes.some((c) => c.length > 20)) errors.accessorials = "Each accessorial code is at most 20 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const body: RateRequest = {
    originZone: v.originZone.trim().toUpperCase(),
    destinationZone: v.destinationZone.trim().toUpperCase(),
    weightKg: v.weightKg.trim(),
    volumeM3: v.volumeM3.trim(),
    pallets: Number(v.pallets.trim()),
    shipDate: v.shipDate,
  };
  if (v.distanceKm.trim() !== "") body.distanceKm = v.distanceKm.trim();
  if (codes.length > 0) body.accessorialCodes = codes;
  return { errors, body };
}

const quoteKey = (q: Quote) => `${q.carrierId}-${q.serviceId}-${q.status}`;

/** Rate a shipment across carrier services, compare, and read the explain trace (FR-34.3, FR-34.4). */
export function RateShopPage() {
  const [values, setValues] = useState<Values>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<RateResponse | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, body } = validateRate(values);
    setErrors(found);
    setFailure(null);
    if (!body) return;
    setBusy(true);
    try {
      const response = await rateShipment(values.shipmentId.trim(), body);
      setResult(response);
      setOpen(response.chosen ? quoteKey(response.chosen) : null);
    } catch (error) {
      setResult(null);
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields as Errors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof Values, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`rate-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id]}
          onChange={(e) => setValues({ ...values, [id]: e.target.value })}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  const chosenKey = result?.chosen ? quoteKey(result.chosen) : null;
  const columns: DataTableColumn<Quote>[] = [
    { key: "carrierId", header: "Carrier", mono: true, render: (q) => <span title={q.carrierId}>{shortId(q.carrierId)}</span> },
    { key: "serviceId", header: "Service", mono: true, render: (q) => <span title={q.serviceId}>{shortId(q.serviceId)}</span> },
    {
      key: "status",
      header: "Status",
      render: (q) => (
        <span className={styles.flags}>
          <Badge tone={q.status === "OK" ? "success" : "warning"}>{statusLabel(q.status)}</Badge>
          {quoteKey(q) === chosenKey ? <Badge tone="info">Cheapest</Badge> : null}
        </span>
      ),
    },
    { key: "charges", header: "Charges", render: (q) => q.charges.map((c) => `${c.code} ${fixed(c.amount, 2)}`).join(", ") || "—" },
    { key: "total", header: "Total", align: "end", render: (q) => money(q.total, q.currency) },
    {
      key: "explain",
      header: "Explain",
      render: (q) => (
        <Button size="sm" variant="ghost" aria-expanded={open === quoteKey(q)} onClick={() => setOpen(open === quoteKey(q) ? null : quoteKey(q))}>
          {open === quoteKey(q) ? "Hide trace" : "Show trace"}
        </Button>
      ),
    },
  ];
  const opened = result?.options.find((q) => quoteKey(q) === open);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rate shop"
        description="Rates the shipment against each carrier service's rate card valid on the ship date. Every option shows how its charges were built."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("shipmentId", "Shipment id", undefined, false, { className: styles.mono })}
          {field("originZone", "Origin zone", "Zone or lane code, such as CHI.")}
          {field("destinationZone", "Destination zone")}
          {field("weightKg", "Weight (kg)", undefined, false, { inputMode: "decimal" })}
          {field("volumeM3", "Volume (m³)", "Sets the dimensional weight.", false, { inputMode: "decimal" })}
          {field("pallets", "Pallets", undefined, false, { inputMode: "numeric" })}
          {field("distanceKm", "Distance (km)", "Needed for cards priced per km or mile.", true, { inputMode: "decimal" })}
          {field("shipDate", "Ship date", undefined, false, { type: "date" })}
          {field("accessorials", "Accessorial codes", "Comma separated, such as LIFTGATE, RESIDENTIAL.", true)}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Rate shop
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "Rate shop failed" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      {busy ? <DataTable label="Rate options" columns={columns} rows={[]} getRowKey={quoteKey} loading /> : null}
      {!busy && result ? (
        result.options.length === 0 ? (
          <Card>
            <EmptyState
              icon="receipt"
              compact
              headingLevel={3}
              title="No rate cards apply"
              description="No carrier service has a rate card. Add rate cards before rate shopping."
            />
          </Card>
        ) : (
          <>
            <p className={styles.note} role="status">
              {result.options.filter((q) => q.status === "OK").length} of {result.options.length} carrier services rated.
            </p>
            <DataTable label="Rate options" columns={columns} rows={result.options} getRowKey={quoteKey} />
          </>
        )
      ) : null}
      {!busy && opened ? (
        <Card title={`Explain trace for ${shortId(opened.carrierId)} / ${shortId(opened.serviceId)}`}>
          <ol className={styles.trace} aria-label="Explain trace">
            {opened.trace.map((step, i) => (
              <li key={i}>
                <span className={styles.mono}>{step.step}</span>: {step.input} = {fixed(step.amount, 2)}
              </li>
            ))}
          </ol>
        </Card>
      ) : null}
    </div>
  );
}
