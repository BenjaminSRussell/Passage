import { FormField, Input, Select } from "../../shell/ui";
import { SOURCE_LABELS, SOURCE_SYSTEMS, SOURCE_SYSTEM_HINT } from "./domain";
import styles from "./pages.module.css";

const CUSTOM = "custom";
const PREFIX = "custom_";

/** A source system choice: one of the four legacy systems, or "Custom system" with a name that becomes custom_<name>. */
export function SourceSystemField({ id, value, error, onChange }: { id: string; value: string; error?: string; onChange(value: string): void }) {
  const custom = value === CUSTOM || value.startsWith(PREFIX);
  return (
    <>
      <FormField id={id} label="Source system" error={custom ? undefined : error}>
        {(control) => (
          <Select
            {...control}
            value={custom ? CUSTOM : value}
            onChange={(event) => onChange(event.target.value === CUSTOM ? PREFIX : event.target.value)}
          >
            <option value="">Choose a system</option>
            {SOURCE_SYSTEMS.map((s) => (
              <option key={s} value={s}>
                {SOURCE_LABELS[s]}
              </option>
            ))}
            <option value={CUSTOM}>Custom system (the customer's own ERP)</option>
          </Select>
        )}
      </FormField>
      {custom ? (
        <FormField id={`${id}-custom`} label="Custom system name" error={error} hint={SOURCE_SYSTEM_HINT}>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              value={value.startsWith(PREFIX) ? value.slice(PREFIX.length) : ""}
              invalid={Boolean(error)}
              onChange={(event) => onChange(PREFIX + event.target.value.trim().toLowerCase())}
            />
          )}
        </FormField>
      ) : null}
    </>
  );
}
