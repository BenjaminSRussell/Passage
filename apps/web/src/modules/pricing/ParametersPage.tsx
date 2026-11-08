import { useRef, useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, EmptyState, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  describeProblem,
  FREE_GOODS_CALC_RULES,
  getPricingParameters,
  newKey,
  PricingApiError,
  setPricingParameters,
  type FreeGoodsCalcRule,
  type PriceSearchMode,
  type PricingDateBasis,
  type PricingParameters,
  type Problem,
  type TotalDiscountBasis,
} from "./api/client";
import { CALC_LABELS } from "./freeGoodsFormat";
import { UUID } from "./format";
import styles from "./pages.module.css";

const DUTY_ADMIN = "pricing.admin";

const DATE_BASES: { value: PricingDateBasis; label: string }[] = [
  { value: "SHIP_DATE", label: "Requested ship date" },
  { value: "ORDER_DATE", label: "Order date" },
  { value: "DELIVERY_DATE", label: "Delivery date" },
];
const TOTAL_BASES: { value: TotalDiscountBasis; label: string }[] = [
  { value: "AFTER_LINE_DISC", label: "After line discounts" },
  { value: "GROSS", label: "Gross, before line discounts" },
];
const SEARCH_MODES: { value: PriceSearchMode; label: string }[] = [
  { value: "FIRST_MATCH", label: "First match" },
  { value: "SEARCH_NEXT", label: "Search next" },
];
const DEFAULT_BANDS: BandRow[] = [
  { upTo: "10", ending: "0.99" },
  { upTo: "100", ending: "0.95" },
  { upTo: "", ending: "0" },
];

interface BandRow {
  upTo: string;
  ending: string;
}

interface ThresholdRow {
  currency: string;
  amount: string;
}

const MAX_THRESHOLDS = 30;

function thresholdRows(table: Record<string, string> | undefined): ThresholdRow[] {
  return Object.entries(table ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([currency, amount]) => ({ currency, amount }));
}

/** Pricing parameters of one legal entity (PRD 5.26.13). Reading needs pricing.view; saving needs pricing.admin. */
export function ParametersPage() {
  const canSave = holdsDuty(usePrincipalDuties(), DUTY_ADMIN);
  const [legalEntityId, setLegalEntityId] = useState("");
  const [idError, setIdError] = useState("");
  const [loaded, setLoaded] = useState<PricingParameters | null>(null);
  const [fourEyes, setFourEyes] = useState(false);
  const [dateBasis, setDateBasis] = useState<PricingDateBasis>("SHIP_DATE");
  const [totalBasis, setTotalBasis] = useState<TotalDiscountBasis>("AFTER_LINE_DISC");
  const [bands, setBands] = useState<BandRow[]>(DEFAULT_BANDS);
  const [searchMode, setSearchMode] = useState<PriceSearchMode>("FIRST_MATCH");
  const [calcRule, setCalcRule] = useState<FreeGoodsCalcRule>("WHOLE_UNITS");
  const [thresholds, setThresholds] = useState<ThresholdRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  /** The field errors of a refused save (422), keyed as the API names them, e.g. smartRoundingBands[1].upTo. */
  const fieldError = (name: string): string | undefined => failure?.fields?.[name];
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
      setDateBasis(params.pricingDateBasis ?? "SHIP_DATE");
      setTotalBasis(params.totalDiscountBasis ?? "AFTER_LINE_DISC");
      setBands(params.smartRoundingBands?.length ? params.smartRoundingBands.map((b) => ({ upTo: b.upTo ?? "", ending: b.ending })) : DEFAULT_BANDS);
      setSearchMode(params.priceSearchMode ?? "FIRST_MATCH");
      setCalcRule(params.freeGoodsCalcRule ?? "WHOLE_UNITS");
      setThresholds(thresholdRows(params.smallOrderThresholds));
      key.current = newKey();
    } catch (error) {
      setLoaded(null);
      setFailure(describeProblem(error));
    } finally {
      setLoading(false);
    }
  }

  /** A changed value is a new command, so it gets a new Idempotency-Key. */
  function edited() {
    setSaved(false);
    key.current = newKey();
  }

  function editBand(index: number, field: keyof BandRow, value: string) {
    setBands((rows) => rows.map((b, i) => (i === index ? { ...b, [field]: value } : b)));
    edited();
  }

  function editThreshold(index: number, field: keyof ThresholdRow, value: string) {
    setThresholds((rows) => rows.map((r, i) => (i === index ? { ...r, [field]: field === "currency" ? value.toUpperCase() : value } : r)));
    edited();
  }

  async function save() {
    if (!loaded) return;
    // Two rows of one currency would collapse into one entry of the table and silently lose a value.
    const seen = new Set<string>();
    const duplicate = thresholds.map((r) => r.currency.trim()).find((c) => c !== "" && (seen.has(c) || !seen.add(c)));
    if (duplicate) {
      setSaved(false);
      setFailure({
        code: "COM-0001",
        message: `The currency ${duplicate} has more than one small order threshold.`,
        fields: { [`smallOrderThresholds[${duplicate}]`]: "List each currency once." },
      });
      return;
    }
    setBusy(true);
    setFailure(null);
    setSaved(false);
    try {
      const next = await setPricingParameters(
        {
          legalEntityId: loaded.legalEntityId,
          journalFourEyes: fourEyes,
          pricingDateBasis: dateBasis,
          totalDiscountBasis: totalBasis,
          smartRoundingBands: bands.map((b, i) => (i === bands.length - 1 ? { ending: b.ending.trim() } : { upTo: b.upTo.trim(), ending: b.ending.trim() })),
          priceSearchMode: searchMode,
          freeGoodsCalcRule: calcRule,
          smallOrderThresholds: Object.fromEntries(thresholds.map((r) => [r.currency.trim(), r.amount.trim()])),
          version: loaded.version,
        },
        key.current,
      );
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
                    edited();
                  }}
                />
                <label htmlFor="pp-four-eyes">Four eyes on journals</label>
              </span>
              <span className={styles.note}>The user who created a price journal cannot post it. Another pricing manager posts it.</span>
            </div>
          </div>
        </Card>
      ) : null}
      {!loading && loaded ? (
        <Card title="Pricing date and total discount">
          <div className={styles.form}>
            <FormField id="pp-date-basis" label="Pricing date basis" error={fieldError("pricingDateBasis")} hint="The document date that prices a request sent without a pricing date. A pricing date sent with the request always wins.">
              {(c) => (
                <Select
                  {...c}
                  value={dateBasis}
                  disabled={!canSave}
                  onChange={(e) => {
                    setDateBasis(e.target.value as PricingDateBasis);
                    edited();
                  }}
                >
                  {DATE_BASES.map((b) => (
                    <option key={b.value} value={b.value}>
                      {b.label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="pp-total-basis" label="Total discount basis" error={fieldError("totalDiscountBasis")} hint="The amount the total discount applies to. Gross ignores the line discounts, and the discount never exceeds the order's net.">
              {(c) => (
                <Select
                  {...c}
                  value={totalBasis}
                  disabled={!canSave}
                  onChange={(e) => {
                    setTotalBasis(e.target.value as TotalDiscountBasis);
                    edited();
                  }}
                >
                  {TOTAL_BASES.map((b) => (
                    <option key={b.value} value={b.value}>
                      {b.label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </Card>
      ) : null}
      {!loading && loaded ? (
        <Card title="Price search">
          <div className={styles.form}>
            <FormField id="pp-search-mode" label="Price search mode" error={fieldError("priceSearchMode")} hint="Search next makes a new condition record continue to the next access when its own is empty. A record that sets it explicitly keeps its value.">
              {(c) => (
                <Select
                  {...c}
                  value={searchMode}
                  disabled={!canSave}
                  onChange={(e) => {
                    setSearchMode(e.target.value as PriceSearchMode);
                    edited();
                  }}
                >
                  {SEARCH_MODES.map((m) => (
                    <option key={m.value} value={m.value}>
                      {m.label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </Card>
      ) : null}
      {!loading && loaded ? (
        <Card title="Free goods">
          <div className={styles.form}>
            <FormField id="pp-calc-rule" label="Free goods calculation rule" error={fieldError("freeGoodsCalcRule")} hint="A new free goods rule that does not name its own calculation takes this one. Rules already saved keep theirs.">
              {(c) => (
                <Select
                  {...c}
                  value={calcRule}
                  disabled={!canSave}
                  onChange={(e) => {
                    setCalcRule(e.target.value as FreeGoodsCalcRule);
                    edited();
                  }}
                >
                  {FREE_GOODS_CALC_RULES.map((k) => (
                    <option key={k} value={k}>
                      {CALC_LABELS[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </Card>
      ) : null}
      {!loading && loaded ? (
        <Card title="Small order thresholds">
          <div className={styles.form}>
            <span className={styles.note}>An order whose net is below the threshold of its currency takes the surcharge. A currency listed here replaces the reference threshold (250 USD, 200 EUR); 0 switches the surcharge off for it.</span>
            {fieldError("smallOrderThresholds") ? (
              <Alert tone="danger" title="Small order thresholds">
                {fieldError("smallOrderThresholds")}
              </Alert>
            ) : null}
            {thresholds.length === 0 ? <span className={styles.note}>No thresholds set: the reference thresholds apply.</span> : null}
            {thresholds.map((r, i) => (
              <div key={i} className={styles.formActions}>
                <FormField id={`pp-threshold-${i}-currency`} label={`Threshold ${i + 1}: currency`} error={fieldError(`smallOrderThresholds[${r.currency}]`)}>
                  {(c) => <Input {...c} className={styles.mono} maxLength={3} value={r.currency} disabled={!canSave} onChange={(e) => editThreshold(i, "currency", e.target.value)} />}
                </FormField>
                <FormField id={`pp-threshold-${i}-amount`} label={`Threshold ${i + 1}: amount`}>
                  {(c) => <Input {...c} className={styles.mono} value={r.amount} disabled={!canSave} onChange={(e) => editThreshold(i, "amount", e.target.value)} />}
                </FormField>
                {canSave ? (
                  <Button
                    type="button"
                    variant="secondary"
                    onClick={() => {
                      setThresholds((rows) => rows.filter((_, j) => j !== i));
                      edited();
                    }}
                  >
                    Remove threshold {i + 1}
                  </Button>
                ) : null}
              </div>
            ))}
            {canSave && thresholds.length < MAX_THRESHOLDS ? (
              <div className={styles.formActions}>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => {
                    setThresholds((rows) => [...rows, { currency: "", amount: "" }]);
                    edited();
                  }}
                >
                  Add threshold
                </Button>
              </div>
            ) : null}
          </div>
        </Card>
      ) : null}
      {!loading && loaded ? (
        <Card title="Smart rounding bands">
          <div className={styles.form}>
            <span className={styles.note}>Journal adjustments with smart rounding round a price up to the next value ending in the band's ending. The last band covers every higher price.</span>
            {fieldError("smartRoundingBands") ? (
              <Alert tone="danger" title="Smart rounding bands">
                {fieldError("smartRoundingBands")}
              </Alert>
            ) : null}
            {bands.map((b, i) => {
              const last = i === bands.length - 1;
              return (
                <div key={i} className={styles.formActions}>
                  {last ? (
                    <span className={styles.note}>{bands.length === 1 ? "All prices" : `From ${bands[i - 1].upTo || "?"}`}</span>
                  ) : (
                    <FormField id={`pp-band-${i}-upto`} label={`Band ${i + 1}: below`} error={fieldError(`smartRoundingBands[${i}].upTo`)}>
                      {(c) => <Input {...c} className={styles.mono} value={b.upTo} disabled={!canSave} onChange={(e) => editBand(i, "upTo", e.target.value)} />}
                    </FormField>
                  )}
                  <FormField id={`pp-band-${i}-ending`} label={`Band ${i + 1}: ending`} error={fieldError(`smartRoundingBands[${i}].ending`)}>
                    {(c) => <Input {...c} className={styles.mono} value={b.ending} disabled={!canSave} onChange={(e) => editBand(i, "ending", e.target.value)} />}
                  </FormField>
                  {canSave && bands.length > 1 ? (
                    <Button
                      type="button"
                      variant="secondary"
                      onClick={() => {
                        setBands((rows) => rows.filter((_, j) => j !== i));
                        edited();
                      }}
                    >
                      Remove band {i + 1}
                    </Button>
                  ) : null}
                </div>
              );
            })}
            {canSave && bands.length < 10 ? (
              <div className={styles.formActions}>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => {
                    setBands((rows) => [...rows.slice(0, -1), { upTo: "", ending: "0.99" }, rows[rows.length - 1]]);
                    edited();
                  }}
                >
                  Add band
                </Button>
              </div>
            ) : null}
          </div>
        </Card>
      ) : null}
      {!loading && loaded ? (
        <Card>
          <div className={styles.form}>
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
