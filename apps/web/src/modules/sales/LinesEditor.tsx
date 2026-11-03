import { Button, FormField, Input, Select } from "../../shell/ui";
import { DELIVERY_DATE_CONTROLS, type DeliveryDateControl } from "./api/client";
import { sentence } from "./format";
import { emptyLine, lineFields, type LineValues } from "./lines";
import styles from "./pages.module.css";

export interface LinesEditorProps {
  lines: LineValues[];
  /** Field errors keyed by the service's paths, such as lines[0].unitPrice. */
  errors: Record<string, string>;
  /** The requested ship date a new line starts with. */
  newLineDate: string;
  onChange(lines: LineValues[]): void;
  /** Offers the per-line delivery date control (orders only; a quotation line has none). */
  withDeliveryControl?: boolean;
}

/** The line grid shared by the order and quotation forms: one fieldset per line. */
export function LinesEditor({ lines, errors, newLineDate, onChange, withDeliveryControl }: LinesEditorProps) {
  const updateLine = (i: number, patch: Partial<LineValues>) => onChange(lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)));
  return (
    <div className={`${styles.formActions} ${styles.lines}`}>
      {lines.map((line, i) => (
        <fieldset key={i} className={styles.lineCard}>
          <legend className={styles.lineLegend}>Line {i + 1}</legend>
          {lineFields.map((f) => {
            const path = `lines[${i}].${f.name}`;
            return (
              <FormField key={f.name} id={`line-${i}-${f.name}`} label={f.label} error={errors[path]}>
                {(control) => (
                  <Input
                    {...control}
                    type={f.type ?? "text"}
                    inputMode={f.inputMode}
                    className={f.mono ? styles.mono : undefined}
                    value={line[f.name]}
                    onChange={(e) => updateLine(i, { [f.name]: e.target.value })}
                    invalid={Boolean(errors[path])}
                  />
                )}
              </FormField>
            );
          })}
          {withDeliveryControl ? (
            <FormField
              id={`line-${i}-deliveryDateControl`}
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
          {lines.length > 1 ? (
            <div className={styles.formActions}>
              <Button size="sm" variant="ghost" onClick={() => onChange(lines.filter((_, idx) => idx !== i))}>
                Remove line {i + 1}
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
          Add line
        </Button>
      </div>
    </div>
  );
}
