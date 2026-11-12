import { useEffect, useRef } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { pickProductPatch } from "../../shell/lookup/entities";
import { Button, FormField, Input, Select } from "../../shell/ui";
import { DELIVERY_DATE_CONTROLS, suggestUnitPrice, type DeliveryDateControl } from "./api/client";
import { sentence } from "./format";
import { emptyLine, lineFields, type LineValues } from "./lines";
import styles from "./pages.module.css";

const UUID_TEXT = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface LinesEditorProps {
  lines: LineValues[];
  /** Field errors keyed by the service's paths, such as lines[0].unitPrice. */
  errors: Record<string, string>;
  /** The requested ship date a new line starts with. */
  newLineDate: string;
  onChange(lines: LineValues[]): void;
  /** Offers the per-line delivery date control (orders only; a quotation line has none). */
  withDeliveryControl?: boolean;
  /** Legend and id prefixes, so a second editor on the same page does not repeat the first one's names. */
  legend?: string;
  idPrefix?: string;
  /** Lets the last line be removed (the change page starts with none). */
  allowEmpty?: boolean;
  addLabel?: string;
  /** The selling legal entity, which narrows the site and warehouse pickers. */
  legalEntityId?: string;
  /** The customer and currency of the order: a product picked on a line with no price yet gets the customer's price. */
  pricing?: { customerId: string; customerGroupId?: string; currency: string };
}

/** The line grid shared by the order and quotation forms: one fieldset per line. */
export function LinesEditor({ lines, errors, newLineDate, onChange, withDeliveryControl, legend = "Line", idPrefix = "line", allowEmpty, addLabel = "Add line", legalEntityId, pricing }: LinesEditorProps) {
  const updateLine = (i: number, patch: Partial<LineValues>) => onChange(lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)));
  // The latest lines, so a price that arrives after the clerk typed on keeps what was typed.
  const latest = useRef(lines);
  latest.current = lines;
  const pricingKey = pricing ? `${pricing.customerId}|${pricing.customerGroupId ?? ""}|${pricing.currency}` : "";
  const canPrice = Boolean(pricing && legalEntityId && UUID_TEXT.test(pricing.customerId) && /^[A-Z]{3}$/.test(pricing.currency));
  // A line is prefilled only while it has no price and no discount of its own: the suggestion is the net price after the
  // customer's discounts, so a discount the clerk entered would count twice.
  const prefillPrice = (i: number, line: LineValues) => {
    if (!pricing || !legalEntityId || !canPrice || line.productId === "" || line.unitPrice !== "" || !(line.discPct === "" || Number(line.discPct) === 0)) return;
    const key = pricingKey;
    void suggestUnitPrice({ legalEntityId, customerId: pricing.customerId, customerGroupId: pricing.customerGroupId, currency: pricing.currency, productId: line.productId, unit: line.unit, quantity: line.quantity }).then(
      (price) => {
        const now = latest.current[i];
        if (price === null || !now || now.productId !== line.productId || now.unitPrice !== "" || latestKey.current !== key) return;
        onChange(latest.current.map((l, idx) => (idx === i ? { ...l, unitPrice: price, pricedFor: key } : l)));
      },
      () => undefined,
    );
  };
  // A change of customer, group or currency drops the prices pricing suggested for the old ones (a typed price stays) and asks again.
  const latestKey = useRef(pricingKey);
  latestKey.current = pricingKey;
  useEffect(() => {
    const stale = latest.current.some((l) => l.pricedFor !== undefined && l.pricedFor !== pricingKey);
    if (!stale) return;
    const cleared = latest.current.map((l) => (l.pricedFor !== undefined && l.pricedFor !== pricingKey ? { ...l, unitPrice: "", pricedFor: undefined } : l));
    onChange(cleared);
    cleared.forEach((l, i) => prefillPrice(i, l));
    // prefillPrice and onChange are rebuilt every render; only the pricing inputs matter here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pricingKey]);
  return (
    <div className={`${styles.formActions} ${styles.lines}`}>
      {lines.map((line, i) => (
        <fieldset key={i} className={styles.lineCard}>
          <legend className={styles.lineLegend}>
            {legend} {i + 1}
          </legend>
          {lineFields.map((f) => {
            const path = `lines[${i}].${f.name}`;
            return (
              <FormField key={f.name} id={`${idPrefix}-${i}-${f.name}`} label={f.label} hint={f.name === "productId" ? line.productHint || undefined : f.name === "unitPrice" && pricing ? "Filled in with the customer's net price (after its discounts) when you pick a product; you can change it." : undefined} error={errors[path]}>
                {(control) =>
                  f.name === "productId" ? (
                    <EntityLookup
                      entity="product"
                      {...control}
                      value={line.productId}
                      onChange={(e) => {
                        const next = { ...line, productId: e.target.value, ...pickProductPatch(line, e.option) };
                        updateLine(i, next);
                        if (e.target.value !== line.productId) prefillPrice(i, next);
                      }}
                      invalid={Boolean(errors[path])}
                    />
                  ) : f.name === "siteId" ? (
                    <EntityLookup
                      entity="site"
                      {...control}
                      value={line.siteId}
                      scope={{ legalEntityId: legalEntityId || undefined }}
                      onChange={(e) => updateLine(i, e.target.value === line.siteId ? {} : { siteId: e.target.value, warehouseId: "" })}
                      invalid={Boolean(errors[path])}
                    />
                  ) : f.name === "warehouseId" ? (
                    <EntityLookup
                      entity="warehouse"
                      {...control}
                      value={line.warehouseId}
                      scope={{ legalEntityId: legalEntityId || undefined, siteId: line.siteId || undefined }}
                      onChange={(e) => updateLine(i, { warehouseId: e.target.value })}
                      invalid={Boolean(errors[path])}
                    />
                  ) : (
                    <Input
                      {...control}
                      type={f.type ?? "text"}
                      inputMode={f.inputMode}
                      className={f.mono ? styles.mono : undefined}
                      value={line[f.name]}
                      onChange={(e) => updateLine(i, f.name === "unitPrice" ? { unitPrice: e.target.value, pricedFor: undefined } : { [f.name]: e.target.value })}
                      invalid={Boolean(errors[path])}
                    />
                  )
                }
              </FormField>
            );
          })}
          {withDeliveryControl ? (
            <FormField
              id={`${idPrefix}-${i}-deliveryDateControl`}
              label="Delivery date control"
              hint="None keeps the requested date; sales lead time confirms no earlier than today plus the lead time."
              error={errors[`lines[${i}].deliveryDateControl`]}
              optional
            >
              {(control) => (
                <Select {...control} value={line.deliveryDateControl ?? ""} onChange={(e) => updateLine(i, { deliveryDateControl: e.target.value as DeliveryDateControl | "" })}>
                  <option value="">Legal entity default</option>
                  {DELIVERY_DATE_CONTROLS.map((c) => (
                    <option key={c} value={c}>
                      {sentence(c)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          {lines.length > 1 || allowEmpty ? (
            <div className={styles.formActions}>
              <Button size="sm" variant="ghost" onClick={() => onChange(lines.filter((_, idx) => idx !== i))}>
                Remove {legend.toLowerCase()} {i + 1}
              </Button>
            </div>
          ) : null}
        </fieldset>
      ))}
      {errors.lines ? (
        <p role="alert" className={styles.fieldError}>
          {errors.lines}
        </p>
      ) : null}
      <div>
        <Button icon="box" onClick={() => onChange([...lines, emptyLine(newLineDate)])}>
          {addLabel}
        </Button>
      </div>
    </div>
  );
}
