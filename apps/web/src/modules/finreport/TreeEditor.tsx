import { useState, type ComponentProps } from "react";
import { Button, Card, FormField, Input, Select } from "../../shell/ui";
import type { Tree, TreeUnit } from "./api/client";
import { move } from "./PartEditors";
import styles from "./pages.module.css";

/** Values are typed as a comma separated list and kept as an array. */
export function parseValues(text: string): string[] {
  return text
    .split(",")
    .map((v) => v.trim())
    .filter((v) => v !== "");
}

/**
 * The values of one unit as comma separated text. The text the user types is kept
 * as typed (a trailing comma survives) and every keystroke updates the tree, so
 * pressing Enter to save never drops values that were typed but not yet blurred.
 */
function ValuesInput({ values, onValues, ...control }: { values: string[]; onValues: (values: string[]) => void } & Omit<ComponentProps<typeof Input>, "value" | "onChange">) {
  const [text, setText] = useState(values.join(", "));
  // Values changed from outside (a reload, a move): show them.
  const shown = parseValues(text).join(",") === values.join(",") ? text : values.join(", ");
  return (
    <Input
      {...control}
      value={shown}
      onChange={(e) => {
        setText(e.target.value);
        onValues(parseValues(e.target.value));
      }}
    />
  );
}

/** The unit cards of a reporting tree (FR-15.4). */
export function TreeUnitsEditor({ tree, onChange }: { tree: Tree; onChange: (patch: Partial<Tree>) => void }) {
  const units = tree.units;
  function changeUnit(i: number, patch: Partial<TreeUnit>) {
    const renamed = patch.code !== undefined && patch.code !== units[i].code ? units[i].code : null;
    onChange({
      units: units.map((u, j) => {
        if (j === i) return { ...u, ...patch };
        // Child units follow a renamed parent instead of pointing at a code that no longer exists.
        if (renamed && u.parent === renamed) return { ...u, parent: patch.code || undefined };
        return u;
      }),
    });
  }
  function remove(i: number) {
    const gone = units[i].code;
    onChange({ units: units.filter((_, j) => j !== i).map((u) => (u.parent === gone ? { ...u, parent: undefined } : u)) });
  }
  return (
    <>
      <Card title="Dimension">
        <div className={styles.form}>
          <FormField id="fr-tree-attribute" label="Dimension attribute" hint="The dimension the units group, such as profit_center. Balances are matched on the value they carry for it.">
            {(c) => <Input {...c} className={styles.mono} value={tree.attribute} onChange={(e) => onChange({ attribute: e.target.value.trim() })} />}
          </FormField>
        </div>
      </Card>
      <Card
        title={`Units (${units.length})`}
        actions={<Button size="sm" onClick={() => onChange({ units: [...units, { code: `U${units.length + 1}`, name: "", values: [] }] })}>Add unit</Button>}
      >
        <p className={styles.note}>A parent unit covers its own values and those of every unit below it; its statement is the roll-up. A value can belong to one unit only.</p>
        <div className={styles.editorGrid}>
          {units.map((unit, i) => (
            <fieldset key={i} className={styles.fieldset}>
              <legend className={styles.legend}>{`Unit ${i + 1}${unit.name ? `: ${unit.name}` : ""}`}</legend>
              <div className={styles.editorLine}>
                <FormField id={`fr-unit-${i}-code`} label="Code">
                  {(c) => <Input {...c} className={styles.mono} value={unit.code} onChange={(e) => changeUnit(i, { code: e.target.value.toUpperCase() })} />}
                </FormField>
                <FormField id={`fr-unit-${i}-name`} label="Unit name">
                  {(c) => <Input {...c} value={unit.name} onChange={(e) => changeUnit(i, { name: e.target.value })} />}
                </FormField>
                <FormField id={`fr-unit-${i}-parent`} label="Parent unit" optional>
                  {(c) => (
                    <Select {...c} value={unit.parent ?? ""} onChange={(e) => changeUnit(i, { parent: e.target.value || undefined })}>
                      <option value="">Top level</option>
                      {units.filter((o, j) => j !== i && o.code !== "").map((o) => (
                        <option key={o.code} value={o.code}>
                          {o.code}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                <FormField id={`fr-unit-${i}-values`} label="Values" optional hint="Separated by commas. Leave empty for a unit that only rolls up its children.">
                  {(c) => <ValuesInput {...c} className={styles.mono} values={unit.values ?? []} onValues={(values) => changeUnit(i, { values })} />}
                </FormField>
              </div>
              <div className={styles.lineActions}>
                <Button size="sm" variant="ghost" onClick={() => onChange({ units: move(units, i, i - 1) })} disabled={i === 0} aria-label={`Move unit ${i + 1} up`}>
                  Up
                </Button>
                <Button size="sm" variant="ghost" onClick={() => onChange({ units: move(units, i, i + 1) })} disabled={i === units.length - 1} aria-label={`Move unit ${i + 1} down`}>
                  Down
                </Button>
                <Button size="sm" variant="ghost" onClick={() => remove(i)} aria-label={`Remove unit ${i + 1}`}>
                  Remove
                </Button>
              </div>
            </fieldset>
          ))}
        </div>
      </Card>
    </>
  );
}
