import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createReservation,
  DEMAND_TYPES,
  describeProblem,
  listReservationCandidates,
  RESERVATION_POLICIES,
  SEGMENT_TYPES,
  type DemandType,
  type Problem,
  type ReservationCandidate,
  type Reservation,
  type ReservationKind,
  type ReservationPolicy,
  type ReserveResult,
  type SegmentType,
} from "./api/client";
import { DECIMAL, describeDimensions, sentence, today, UUID } from "./format";
import { ALL_DIMENSIONS, emptyDimensions, fieldErrorsOf, FORM_DIMENSIONS, type FormDimension } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface ReservationValues {
  legalEntityId: string;
  productId: string;
  qty: string;
  policy: ReservationPolicy;
  demandType: DemandType;
  demandId: string;
  requiredDate: string;
  shelfLifeMarginDays: string;
  /** Hours after which the reservation lapses; empty or 0 means no expiry (never "expire now"). */
  ttlHours: string;
  allowPartial: boolean;
  dimensions: Record<FormDimension, string>;
  /** Segment of the demand (FR-16.20): sales-order or project stock. */
  segmentType: SegmentType;
  segmentRef: string;
  segmentFallback: boolean;
  /** What the reservation holds: stock on hand (default) or part of an expected receipt (manual, one dimension set). */
  against?: ReservationKind;
}

const fields = [
  "legalEntityId",
  "productId",
  "qty",
  "policy",
  "demandType",
  "demandId",
  "requiredDate",
  "shelfLifeMarginDays",
  "ttlHours",
  "dimensions",
  "segmentType",
  "segmentRef",
  "against",
] as const;
type Field = (typeof fields)[number];
type Errors = Partial<Record<Field, string>>;

const policyHints: Record<ReservationPolicy, string> = {
  FIFO: "Oldest receipts first, across the dimension sets that match the dimensions you enter.",
  FEFO: "Earliest expiry first; batches expiring inside the shelf-life margin are skipped.",
  LIFO: "Newest receipts first.",
  MANUAL: "Exactly the dimension set you enter, including its site.",
};

/** Client checks that mirror the service, so most mistakes never leave the form. */
export function validateReservation(values: ReservationValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(values.productId.trim())) errors.productId = "Enter the product id (a UUID).";
  const qty = values.qty.trim();
  if (!DECIMAL.test(qty) || Number(qty) <= 0) errors.qty = "Enter a quantity above zero.";
  if (values.demandId.trim() === "") errors.demandId = "Enter the order or reference the stock is held for.";
  const margin = values.shelfLifeMarginDays.trim();
  if (margin !== "" && (!/^\d+$/.test(margin) || Number(margin) > 365)) errors.shelfLifeMarginDays = "Enter 0 to 365 days.";
  const ttl = values.ttlHours.trim();
  if (ttl !== "" && (!/^\d+$/.test(ttl) || Number(ttl) > 8760)) errors.ttlHours = "Enter 1 to 8760 hours, or leave it empty (or 0) for no expiry.";
  if (values.policy === "MANUAL" && values.dimensions.site.trim() === "") errors.dimensions = "A manual reservation names the site and every dimension of the stock.";
  if (values.against === "ORDERED" && values.policy !== "MANUAL") errors.against = "A reservation against an expected receipt is manual: it names the dimension set that receives.";
  const ref = values.segmentRef.trim();
  if (values.segmentType !== "NONE" && (ref === "" || ref.length > 48)) errors.segmentRef = "Enter the sales order or project reference (up to 48 characters).";
  return errors;
}

const allocationColumns: DataTableColumn<Reservation>[] = [
  { key: "allocationNo", header: "Allocation", align: "end" },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "qty", header: "Quantity", align: "end", mono: true },
];

const reasonText: Record<string, string> = {
  RESTRICTED: "Blocked or in quarantine",
  EXPIRES_BEFORE: "Expires before the required date plus the margin",
  OTHER_SEGMENT: "Stock of another segment",
};

/** The listed stock and the legal entity, product and demand segment it was listed for ("" when unsegmented). */
type Stock =
  | { state: "idle" }
  | { state: "loading" }
  | { state: "error"; problem: Problem }
  | { state: "ready"; rows: ReservationCandidate[]; scope: string; segment: string };

/** The segment dimension value of the form's demand ("" when unsegmented). */
const demandSegment = (values: ReservationValues): string =>
  values.segmentType === "NONE" ? "" : `${values.segmentType}:${values.segmentRef.trim()}`;

/** What a candidate list depends on beyond the policy and the filters: picking from a list of another product would reserve the wrong stock. */
const stockScope = (values: ReservationValues): string =>
  [values.legalEntityId.trim(), values.productId.trim(), demandSegment(values)].join("|");

/**
 * A candidate can be picked into the form when the manual reservation the form submits names exactly its
 * dimension set: only the form's own dimensions, plus the segment the form adds for a segmented demand.
 */
const pickable = (row: ReservationCandidate, segment: string): boolean =>
  row.eligible &&
  (row.dimensions.segment ?? "") === segment &&
  Object.keys(row.dimensions).every((name) => name === "segment" || (FORM_DIMENSIONS as readonly string[]).includes(name));

const stockColumns = (pick: (row: ReservationCandidate) => void, segment: string): DataTableColumn<ReservationCandidate>[] => [
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "available", header: "Available", align: "end", mono: true },
  { key: "expiryDate", header: "Expires", render: (row) => row.expiryDate ?? "No expiry" },
  { key: "eligible", header: "Policy", render: (row) => (row.eligible ? "Eligible" : (reasonText[row.reason ?? ""] ?? "Not eligible")) },
  {
    key: "pick",
    header: "Action",
    render: (row) =>
      pickable(row, segment) ? (
        <Button type="button" size="sm" variant="secondary" onClick={() => pick(row)}>
          Reserve this stock
        </Button>
      ) : null,
  },
];

const blank = (): ReservationValues => ({
  legalEntityId: "",
  productId: "",
  qty: "",
  policy: "FIFO",
  demandType: "SALES_ORDER",
  demandId: "",
  requiredDate: today(),
  shelfLifeMarginDays: "0",
  ttlHours: "",
  allowPartial: false,
  dimensions: emptyDimensions(),
  segmentType: "NONE",
  segmentRef: "",
  segmentFallback: true,
});

const segmentHints: Record<SegmentType, string> = {
  NONE: "Unsegmented demand takes unsegmented stock only.",
  SALES_ORDER: "Sales-order stock of this reference is taken first.",
  PROJECT: "Project stock of this reference is taken first.",
};

/** Reservation dialog (PRD 3.16.6): reserve by policy or for one exact dimension set. */
export function NewReservationPage() {
  const [values, setValues] = useState<ReservationValues>(blank);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ReserveResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [stock, setStock] = useState<Stock>({ state: "idle" });
  const [stockError, setStockError] = useState<Errors>({});
  // One key per filled-in form: a double submit replays instead of reserving twice.
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateReservation(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    const segmented = values.segmentType !== "NONE";
    const segment = demandSegment(values);
    try {
      const out = await createReservation(
        {
          legalEntityId: values.legalEntityId.trim(),
          productId: values.productId.trim(),
          qty: values.qty.trim(),
          policy: values.policy,
          demandType: values.demandType,
          demandId: values.demandId.trim(),
          requiredDate: values.requiredDate,
          shelfLifeMarginDays: Number(values.shelfLifeMarginDays || "0"),
          ttlHours: Number(values.ttlHours || "0"),
          against: values.against,
          allowPartial: values.allowPartial,
          // A manual reservation of a segmented demand reserves that segment's stock.
          dimensions: values.policy === "MANUAL" && segmented ? { ...values.dimensions, segment } : values.dimensions,
          segmentType: values.segmentType,
          segmentRef: values.segmentRef,
          segmentFallback: values.segmentFallback,
        },
        key.current,
      );
      setResult(out);
      key.current = crypto.randomUUID();
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = fieldErrorsOf(problem, fields);
      if (Object.keys(mapped).length > 0) setErrors(mapped);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  async function findStock() {
    const found: Errors = {};
    if (!UUID.test(values.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (!UUID.test(values.productId.trim())) found.productId = "Enter the product id (a UUID).";
    setStockError(found);
    if (Object.keys(found).length > 0) return;
    setStock({ state: "loading" });
    try {
      const rows = await listReservationCandidates({
        legalEntityId: values.legalEntityId.trim(),
        productId: values.productId.trim(),
        policy: values.policy,
        requiredDate: values.requiredDate,
        shelfLifeMarginDays: Number(values.shelfLifeMarginDays || "0"),
        segmentType: values.segmentType,
        segmentRef: values.segmentRef,
        segmentFallback: values.segmentFallback,
        dimensions: values.dimensions,
      });
      setStock({ state: "ready", rows, scope: stockScope(values), segment: demandSegment(values) });
    } catch (error) {
      setStock({ state: "error", problem: describeProblem(error) });
    }
  }

  // Picking a row reserves exactly that dimension set (a manual reservation).
  function pick(row: ReservationCandidate) {
    const dimensions = emptyDimensions();
    for (const name of FORM_DIMENSIONS) dimensions[name] = row.dimensions[name] ?? "";
    change({ ...values, policy: "MANUAL", dimensions });
  }

  const change = (next: ReservationValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: Exclude<keyof ReservationValues, "dimensions" | "allowPartial" | "segmentFallback" | "against">) => (event: { target: { value: string } }) =>
    change({ ...values, [name]: event.target.value });
  const setDim = (name: FormDimension) => (event: { target: { value: string } }) =>
    change({ ...values, dimensions: { ...values.dimensions, [name]: event.target.value } });

  return (
    <div className={styles.stack}>
      <PageHeader title="New reservation" description="Hold available stock for a demand so it cannot be issued elsewhere." />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="res-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="res-product" label="Product" error={errors.productId}>
            {(control) => <Input {...control} value={values.productId} onChange={set("productId")} invalid={Boolean(errors.productId)} />}
          </FormField>
          <FormField id="res-qty" label="Quantity" error={errors.qty}>
            {(control) => <Input {...control} inputMode="decimal" value={values.qty} onChange={set("qty")} invalid={Boolean(errors.qty)} />}
          </FormField>
          <FormField
            id="res-against"
            label="Reserve"
            hint={values.against === "ORDERED" ? "Part of the quantity still expected to arrive at the dimension set below, less what is already reserved against it." : "Stock that is on hand now."}
            error={errors.against}
          >
            {(control) => (
              <Select
                {...control}
                value={values.against ?? "PHYSICAL"}
                onChange={(event) => {
                  const against = event.target.value as ReservationKind;
                  change({ ...values, against, policy: against === "ORDERED" ? "MANUAL" : values.policy });
                }}
              >
                <option value="PHYSICAL">Stock on hand</option>
                <option value="ORDERED">Expected receipt</option>
              </Select>
            )}
          </FormField>
          <FormField id="res-policy" label="Policy" hint={policyHints[values.policy]}>
            {(control) => (
              <Select {...control} value={values.policy} onChange={set("policy")}>
                {RESERVATION_POLICIES.map((p) => (
                  <option key={p} value={p}>
                    {p === "MANUAL" ? "Manual" : p}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="res-demand-type" label="Demand type">
            {(control) => (
              <Select {...control} value={values.demandType} onChange={set("demandType")}>
                {DEMAND_TYPES.map((d) => (
                  <option key={d} value={d}>
                    {sentence(d)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="res-demand-id" label="Demand reference" error={errors.demandId}>
            {(control) => <Input {...control} value={values.demandId} maxLength={60} onChange={set("demandId")} invalid={Boolean(errors.demandId)} />}
          </FormField>
          <FormField id="res-required" label="Required date" error={errors.requiredDate}>
            {(control) => <Input {...control} type="date" value={values.requiredDate} onChange={set("requiredDate")} />}
          </FormField>
          <FormField id="res-margin" label="Shelf-life margin (days)" optional error={errors.shelfLifeMarginDays}>
            {(control) => (
              <Input {...control} inputMode="numeric" value={values.shelfLifeMarginDays} onChange={set("shelfLifeMarginDays")} invalid={Boolean(errors.shelfLifeMarginDays)} />
            )}
          </FormField>
          <FormField id="res-ttl" label="Expires after (hours)" optional hint="Empty or 0: no expiry, the stock stays reserved until you release it." error={errors.ttlHours}>
            {(control) => <Input {...control} inputMode="numeric" value={values.ttlHours} onChange={set("ttlHours")} invalid={Boolean(errors.ttlHours)} />}
          </FormField>
          <FormField id="res-segment-type" label="Demand segment" hint={segmentHints[values.segmentType]} error={errors.segmentType}>
            {(control) => (
              <Select {...control} value={values.segmentType} onChange={set("segmentType")}>
                {SEGMENT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t === "NONE" ? "None" : sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.segmentType !== "NONE" ? (
            <FormField id="res-segment-ref" label="Segment reference" error={errors.segmentRef}>
              {(control) => <Input {...control} value={values.segmentRef} maxLength={48} onChange={set("segmentRef")} invalid={Boolean(errors.segmentRef)} />}
            </FormField>
          ) : null}
          {FORM_DIMENSIONS.map((name) => (
            <FormField
              key={name}
              id={`res-${name}`}
              label={sentence(name)}
              optional={!(name === "site" && values.policy === "MANUAL")}
              error={name === "site" ? errors.dimensions : undefined}
            >
              {(control) => (
                <Input {...control} value={values.dimensions[name]} maxLength={60} onChange={setDim(name)} invalid={name === "site" && Boolean(errors.dimensions)} />
              )}
            </FormField>
          ))}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input
                type="checkbox"
                id="res-partial"
                checked={values.allowPartial}
                onChange={(event) => change({ ...values, allowPartial: event.target.checked })}
              />
              <label htmlFor="res-partial">Reserve what is available if not all of it is</label>
            </span>
            {values.segmentType !== "NONE" ? (
              <span className={styles.check}>
                <Input
                  type="checkbox"
                  id="res-segment-fallback"
                  checked={values.segmentFallback}
                  onChange={(event) => change({ ...values, segmentFallback: event.target.checked })}
                />
                <label htmlFor="res-segment-fallback">Use unsegmented stock when the segment runs short</label>
              </span>
            ) : null}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not reserve" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Reserve
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/reservations">
              Back to reservations
            </Link>
          </div>
        </form>
      </Card>
      <Card title="Available stock">
        <div className={styles.stack}>
          <p>Lists the batches and serials of the product in the order the policy would take them. The dimensions above narrow the list.</p>
          {stockError.legalEntityId || stockError.productId ? (
            <Alert tone="danger" live="alert">
              {stockError.legalEntityId ?? stockError.productId}
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button type="button" variant="secondary" loading={stock.state === "loading"} onClick={findStock}>
              Show available stock
            </Button>
          </div>
          {stock.state === "error" ? (
            <Alert tone="danger" title="Could not load the stock" live="alert">
              <ProblemMessage problem={stock.problem} />
            </Alert>
          ) : null}
          {stock.state === "ready" && stock.scope !== stockScope(values) ? (
            <Alert tone="warning">The legal entity, product or segment changed. Show the available stock again before picking a row.</Alert>
          ) : null}
          {stock.state === "ready" && stock.scope === stockScope(values) ? (
            <DataTable
              label="Available stock"
              columns={stockColumns(pick, stock.segment)}
              rows={stock.rows}
              getRowKey={(row) => JSON.stringify(row.dimensions)}
              empty={{ title: "No available stock", description: "Nothing of this product has available quantity for these filters." }}
            />
          ) : null}
        </div>
      </Card>
      {result ? (
        <Card title="Reserved">
          <div className={styles.stack}>
            <Alert tone={Number(result.shortfall) > 0 ? "warning" : "success"} live="status">
              {Number(result.shortfall) > 0
                ? `Reserved part of the request; ${result.shortfall} could not be reserved.`
                : `Reserved in ${result.items.length} ${result.items.length === 1 ? "allocation" : "allocations"}.`}
            </Alert>
            <DataTable
              label="Allocations"
              columns={allocationColumns}
              rows={result.items}
              getRowKey={(row) => row.id}
              empty={{ title: "Nothing reserved" }}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
