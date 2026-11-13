import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { describeProblem, listTruckStockLevels, setTruckStockLevels, type Problem, type TruckStockLevel } from "./api/client";
import { DECIMAL, UUID } from "./format";
import { ProblemAlert } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const DUTY_TRUCK_STOCK_WRITE = "inventory.truck-stock.write";

export interface TruckLevelLine {
  productId: string;
  unit: string;
  minQty: string;
  maxQty: string;
}

export interface TruckLevelsValues {
  /** The codes the truck is refilled from; the lookups hold the picked record, the codes are what the service keeps. */
  sourceSiteCode: string;
  sourceWarehouseCode: string;
  lines: TruckLevelLine[];
}

type HeaderField = "legalEntityId" | "siteId" | "warehouseId" | "sourceSiteId" | "sourceWarehouseId" | "levels";
type LineField = keyof TruckLevelLine;

export interface TruckLevelsErrors {
  header: Partial<Record<HeaderField, string>>;
  lines: Partial<Record<LineField, string>>[];
}

const UNIT = /^[A-Z0-9_-]{1,12}$/;
const CODE = /^\S{1,60}$/;
const emptyLine = (): TruckLevelLine => ({ productId: "", unit: "EA", minQty: "", maxQty: "" });

/** Client checks that mirror the service (INV truck levels): codes, units, a minimum of 0 or more and a maximum not below it. */
export function validateTruckLevels(truck: { legalEntityId: string; siteId: string; warehouseId: string; sourceWarehouseId: string }, values: TruckLevelsValues): TruckLevelsErrors {
  const header: TruckLevelsErrors["header"] = {};
  if (!UUID.test(truck.legalEntityId)) header.legalEntityId = "Choose the legal entity.";
  if (!UUID.test(truck.siteId)) header.siteId = "Choose the truck's site.";
  if (!UUID.test(truck.warehouseId)) header.warehouseId = "Choose the truck.";
  if (!CODE.test(values.sourceSiteCode.trim())) header.sourceSiteId = "Choose the site the truck is refilled from.";
  if (!CODE.test(values.sourceWarehouseCode.trim())) header.sourceWarehouseId = "Choose the warehouse the truck is refilled from.";
  else if (truck.sourceWarehouseId !== "" && truck.sourceWarehouseId === truck.warehouseId) header.sourceWarehouseId = "The truck cannot be refilled from itself.";
  if (values.lines.length > 500) header.levels = "A truck holds at most 500 levels.";
  const seen = new Set<string>();
  const lines = values.lines.map((line) => {
    const errors: Partial<Record<LineField, string>> = {};
    const product = line.productId.trim().toLowerCase();
    if (!UUID.test(product)) errors.productId = "Choose the product.";
    else if (seen.has(product)) errors.productId = "This product already has a level on this truck.";
    seen.add(product);
    if (!UNIT.test(line.unit.trim())) errors.unit = "Enter a unit code such as EA.";
    const min = line.minQty.trim();
    const max = line.maxQty.trim();
    if (!DECIMAL.test(min) || Number(min) < 0) errors.minQty = "Enter a minimum of 0 or more.";
    if (!DECIMAL.test(max) || Number(max) <= 0) errors.maxQty = "Enter a maximum above zero.";
    else if (DECIMAL.test(min) && Number(max) < Number(min)) errors.maxQty = "The maximum cannot be below the minimum.";
    return errors;
  });
  return { header, lines };
}

function hasErrors(errors: TruckLevelsErrors): boolean {
  return Object.keys(errors.header).length > 0 || errors.lines.some((l) => Object.keys(l).length > 0);
}

/** Maps the service's field errors (sourceWarehouseId, levels[2], ...) onto the form. */
function mapProblem(problem: Problem, lineCount: number): TruckLevelsErrors | null {
  const out: TruckLevelsErrors = { header: {}, lines: Array.from({ length: lineCount }, () => ({})) };
  let found = false;
  for (const [name, message] of Object.entries(problem.fields)) {
    const line = /^levels\[(\d+)\]/.exec(name);
    if (line) {
      const i = Number(line[1]);
      if (out.lines[i]) {
        const field: LineField = /unit/.test(message) ? "unit" : /qty/i.test(message) ? "maxQty" : "productId";
        out.lines[i][field] = message;
        found = true;
      }
    } else if (["legalEntityId", "siteId", "warehouseId", "sourceSiteId", "sourceWarehouseId", "levels"].includes(name)) {
      out.header[name as HeaderField] = message;
      found = true;
    }
  }
  return found ? out : null;
}

function fromLevels(levels: TruckStockLevel[]): TruckLevelsValues {
  const first = levels[0];
  return {
    sourceSiteCode: first?.sourceSiteId ?? "",
    sourceWarehouseCode: first?.sourceWarehouseId ?? "",
    lines: levels.length > 0 ? levels.map((l) => ({ productId: l.productId, unit: l.unit, minQty: l.minQty, maxQty: l.maxQty })) : [emptyLine()],
  };
}

/**
 * Set truck stock levels (FR-45.9): pick a technician's truck, the warehouse it is refilled from, and the minimum and
 * maximum of each product it carries. Saving replaces the truck's levels; a product left out is no longer stocked.
 */
export function TruckLevelsPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [legalEntityId, setLegalEntityId] = useState(params.get("legalEntityId") ?? "");
  const [siteId, setSiteId] = useState(params.get("siteId") ?? "");
  const [warehouseId, setWarehouseId] = useState(params.get("warehouseId") ?? "");
  const [sourceSiteId, setSourceSiteId] = useState("");
  const [sourceWarehouseId, setSourceWarehouseId] = useState("");
  const [values, setValues] = useState<TruckLevelsValues>({ sourceSiteCode: "", sourceWarehouseCode: "", lines: [emptyLine()] });
  const [errors, setErrors] = useState<TruckLevelsErrors>({ header: {}, lines: [] });
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const entity = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const truck = UUID.test(warehouseId.trim()) ? warehouseId.trim() : "";
  const load = useLoad((signal) => (entity && truck ? listTruckStockLevels(entity, truck, signal) : Promise.resolve(null)), `${entity}|${truck}`);
  const existing = load.data;

  // The truck's current levels fill the form once they arrive, so saving starts from what the truck has today.
  useEffect(() => {
    if (existing === null) return;
    const mine = existing.filter((l) => l.warehouseId.toLowerCase() === truck.toLowerCase());
    setValues(fromLevels(mine));
    setSourceSiteId("");
    setSourceWarehouseId("");
    setErrors({ header: {}, lines: [] });
    if (mine[0] && !UUID.test(siteId)) setSiteId(mine[0].siteId);
    // siteId only seeds an empty site; reloading on it would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [existing, truck]);

  const setLine = (index: number, patch: Partial<TruckLevelLine>) => setValues({ ...values, lines: values.lines.map((line, i) => (i === index ? { ...line, ...patch } : line)) });

  async function submit(event: { preventDefault(): void }) {
    event.preventDefault();
    const truckRef = { legalEntityId: entity, siteId: siteId.trim().toLowerCase(), warehouseId: truck.toLowerCase(), sourceWarehouseId: sourceWarehouseId.toLowerCase() };
    const found = validateTruckLevels(truckRef, values);
    setErrors(found);
    setFailure(null);
    if (hasErrors(found)) return;
    setBusy(true);
    try {
      await setTruckStockLevels({
        legalEntityId: truckRef.legalEntityId,
        siteId: truckRef.siteId,
        warehouseId: truckRef.warehouseId,
        sourceSiteId: values.sourceSiteCode.trim(),
        sourceWarehouseId: values.sourceWarehouseCode.trim(),
        levels: values.lines.map((line) => ({ productId: line.productId.trim().toLowerCase(), unit: line.unit.trim(), minQty: line.minQty.trim(), maxQty: line.maxQty.trim() })),
      });
      navigate(`/inventory/truck-stock?${new URLSearchParams({ legalEntityId: truckRef.legalEntityId, warehouseId: truckRef.warehouseId }).toString()}`);
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = mapProblem(problem, values.lines.length);
      if (mapped) setErrors(mapped);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const he = errors.header;
  const ready = entity !== "" && truck !== "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Set truck stock levels"
        description="Choose a technician's truck and the minimum and maximum of each part it carries. A truck below a minimum is refilled up to the maximum from the warehouse you name here."
      />
      <form onSubmit={submit} noValidate className={styles.stack}>
        <Card title="Truck">
          <div className={styles.form}>
            <LookupFormField id="tl-entity" label="Legal entity" entity="legalEntity" value={legalEntityId} onChange={setLegalEntityId} error={he.legalEntityId} />
            <LookupFormField id="tl-site" label="Truck site" entity="site" scope={{ legalEntityId: entity || undefined }} value={siteId} onChange={setSiteId} error={he.siteId} />
            <LookupFormField
              id="tl-truck"
              label="Truck"
              entity="warehouse"
              scope={{ legalEntityId: entity || undefined, siteId: UUID.test(siteId) ? siteId : undefined }}
              prefill={false}
              value={warehouseId}
              onChange={setWarehouseId}
              hint="Each truck is a warehouse of its own."
              error={he.warehouseId}
            />
          </div>
        </Card>
        {!ready ? (
          <Alert tone="info" title="Choose a truck">
            Pick the legal entity and the truck whose levels you want to set. Its current levels are filled in for you.
          </Alert>
        ) : load.error ? (
          <ProblemAlert title="Could not load the truck's levels" problem={load.error} onRetry={load.reload} />
        ) : load.loading && existing === null ? (
          <p className={styles.muted}>Loading the truck's levels…</p>
        ) : (
          <>
            <Card title="Refilled from">
              <div className={styles.form}>
                <LookupFormField
                  id="tl-source-site"
                  label="Source site"
                  entity="site"
                  scope={{ legalEntityId: entity }}
                  prefill={false}
                  value={sourceSiteId}
                  onChange={(id, option) => {
                    setSourceSiteId(id);
                    setValues({ ...values, sourceSiteCode: option?.number ?? "" });
                  }}
                  hint={values.sourceSiteCode && !sourceSiteId ? `Now ${values.sourceSiteCode}. Pick another site to change it.` : undefined}
                  error={he.sourceSiteId}
                />
                <LookupFormField
                  id="tl-source-warehouse"
                  label="Source warehouse"
                  entity="warehouse"
                  scope={{ legalEntityId: entity, siteId: UUID.test(sourceSiteId) ? sourceSiteId : undefined }}
                  prefill={false}
                  value={sourceWarehouseId}
                  onChange={(id, option) => {
                    setSourceWarehouseId(id);
                    setValues({ ...values, sourceWarehouseCode: option?.number ?? "" });
                  }}
                  hint={values.sourceWarehouseCode && !sourceWarehouseId ? `Now ${values.sourceWarehouseCode}. Pick another warehouse to change it.` : undefined}
                  error={he.sourceWarehouseId}
                />
              </div>
            </Card>
            {values.lines.map((line, index) => {
              const le = errors.lines[index] ?? {};
              const n = index + 1;
              return (
                <Card key={index} title={`Part ${n}`}>
                  <div className={styles.form}>
                    <LookupFormField
                      id={`tl-line-${n}-product`}
                      label="Product"
                      entity="product"
                      prefill={false}
                      value={line.productId}
                      onChange={(id, option) => setLine(index, { productId: id, ...(option?.unit ? { unit: option.unit.toUpperCase() } : {}) })}
                      error={le.productId}
                    />
                    <FormField id={`tl-line-${n}-unit`} label="Unit" hint="The product's inventory unit." error={le.unit}>
                      {(control) => <Input {...control} value={line.unit} maxLength={12} onChange={(e) => setLine(index, { unit: e.target.value.toUpperCase() })} invalid={Boolean(le.unit)} />}
                    </FormField>
                    <FormField id={`tl-line-${n}-min`} label="Minimum" hint="Below this the truck is refilled." error={le.minQty}>
                      {(control) => <Input {...control} inputMode="decimal" value={line.minQty} onChange={(e) => setLine(index, { minQty: e.target.value })} invalid={Boolean(le.minQty)} />}
                    </FormField>
                    <FormField id={`tl-line-${n}-max`} label="Maximum" hint="A refill brings the truck up to this." error={le.maxQty}>
                      {(control) => <Input {...control} inputMode="decimal" value={line.maxQty} onChange={(e) => setLine(index, { maxQty: e.target.value })} invalid={Boolean(le.maxQty)} />}
                    </FormField>
                    <div className={styles.formActions}>
                      <Button size="sm" onClick={() => setValues({ ...values, lines: values.lines.filter((_, i) => i !== index) })}>
                        Remove part {n}
                      </Button>
                    </div>
                  </div>
                </Card>
              );
            })}
            {values.lines.length === 0 ? (
              <Alert tone="warning" title="No parts">
                Saving without parts removes every level of this truck, so it is no longer refilled.
              </Alert>
            ) : null}
            {he.levels ? (
              <Alert tone="danger" title="Too many parts" live="alert">
                {he.levels}
              </Alert>
            ) : null}
            {failure ? <ProblemAlert title="Could not save the truck's levels" problem={failure} /> : null}
            <div className={styles.formActions}>
              <Button onClick={() => setValues({ ...values, lines: [...values.lines, emptyLine()] })}>Add part</Button>
              <Button type="submit" variant="primary" loading={busy}>
                Save levels
              </Button>
              <Link className={buttonClassName("ghost")} to={`/inventory/truck-stock${entity ? `?legalEntityId=${entity}` : ""}`}>
                Cancel
              </Link>
            </div>
          </>
        )}
      </form>
    </div>
  );
}
