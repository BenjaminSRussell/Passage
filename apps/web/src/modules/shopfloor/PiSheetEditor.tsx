import { Button, FormField, Input, Select, Textarea } from "../../shell/ui";
import type { Instruction, InstructionCategory, RecipeDocument, RecipeOperation, RecipePhase, RecipeUnitProcedure } from "./api/client";
import { CATEGORIES, CATEGORY_LABEL, moveItem, newInstruction, renumber } from "./piSheet";
import styles from "./pages.module.css";

/** Edits the ISA-88 structure of a PI sheet: unit procedures, operations, phases and their numbered instructions (FR-25.16). */
export function PiSheetEditor({ doc, onChange, readOnly }: { doc: RecipeDocument; onChange(next: RecipeDocument): void; readOnly: boolean }) {
  const ups = doc.procedure.unitProcedures;
  const setUps = (next: RecipeUnitProcedure[]) => onChange({ procedure: { ...doc.procedure, unitProcedures: next } });
  const patch = <T,>(list: T[], i: number, change: Partial<T>): T[] => list.map((item, j) => (j === i ? { ...item, ...change } : item));
  return (
    <div className={styles.stack}>
      <FormField id="pi-procedure" label="Procedure name" hint="The batch or process this sheet runs.">
        {(control) => <Input {...control} value={doc.procedure.name} disabled={readOnly} maxLength={120} onChange={(e) => onChange({ procedure: { ...doc.procedure, name: e.target.value } })} />}
      </FormField>
      {ups.map((up, ui) => (
        <fieldset key={ui} className={styles.fieldset}>
          <legend>{`Unit procedure ${ui + 1}`}</legend>
          <FormField id={`pi-up-${ui}`} label="Unit procedure name">
            {(control) => <Input {...control} value={up.name} disabled={readOnly} maxLength={120} onChange={(e) => setUps(patch(ups, ui, { name: e.target.value }))} />}
          </FormField>
          {up.operations.map((op, oi) => (
            <fieldset key={oi} className={styles.fieldset}>
              <legend>{`Operation ${oi + 1}`}</legend>
              <FormField id={`pi-op-${ui}-${oi}`} label="Operation name">
                {(control) => (
                  <Input
                    {...control}
                    value={op.name}
                    disabled={readOnly}
                    maxLength={120}
                    onChange={(e) => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { name: e.target.value }) }))}
                  />
                )}
              </FormField>
              {op.phases.map((ph, pi) => (
                <PhaseEditor
                  key={pi}
                  id={`${ui}-${oi}-${pi}`}
                  phase={ph}
                  readOnly={readOnly}
                  canRemove={op.phases.length > 1}
                  onChange={(next) => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { phases: patch(op.phases, pi, next) }) }))}
                  onRemove={() => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { phases: op.phases.filter((_, k) => k !== pi) }) }))}
                />
              ))}
              {readOnly ? null : (
                <div className={styles.actions}>
                  <Button
                    size="sm"
                    onClick={() => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { phases: [...op.phases, { name: `Phase ${op.phases.length + 1}`, instructions: [newInstruction("instruction-text", 1)] }] }) }))}
                  >
                    Add phase
                  </Button>
                  {up.operations.length > 1 ? (
                    <Button size="sm" variant="ghost" onClick={() => setUps(patch(ups, ui, { operations: up.operations.filter((_, k) => k !== oi) }))}>
                      Remove this operation
                    </Button>
                  ) : null}
                </div>
              )}
            </fieldset>
          ))}
          {readOnly ? null : (
            <div className={styles.actions}>
              <Button
                size="sm"
                onClick={() => setUps(patch(ups, ui, { operations: [...up.operations, emptyOperation(up.operations.length + 1)] }))}
              >
                Add operation
              </Button>
              {ups.length > 1 ? (
                <Button size="sm" variant="ghost" onClick={() => setUps(ups.filter((_, k) => k !== ui))}>
                  Remove this unit procedure
                </Button>
              ) : null}
            </div>
          )}
        </fieldset>
      ))}
      {readOnly ? null : (
        <div className={styles.actions}>
          <Button onClick={() => setUps([...ups, { name: `Unit ${ups.length + 1}`, operations: [emptyOperation(1)] }])}>Add unit procedure</Button>
        </div>
      )}
    </div>
  );
}

function emptyOperation(n: number): RecipeOperation {
  return { name: `Operation ${n}`, phases: [{ name: "Phase 1", instructions: [newInstruction("instruction-text", 1)] }] };
}

function PhaseEditor({
  id,
  phase,
  readOnly,
  canRemove,
  onChange,
  onRemove,
}: {
  id: string;
  phase: RecipePhase;
  readOnly: boolean;
  canRemove: boolean;
  onChange(next: Partial<RecipePhase>): void;
  onRemove(): void;
}) {
  const list = phase.instructions;
  const setList = (next: Instruction[]) => onChange({ instructions: renumber(next) });
  return (
    <fieldset className={styles.fieldset}>
      <legend>{`Phase: ${phase.name || "unnamed"}`}</legend>
      <FormField id={`pi-ph-${id}`} label="Phase name">
        {(control) => <Input {...control} value={phase.name} disabled={readOnly} maxLength={120} onChange={(e) => onChange({ name: e.target.value })} />}
      </FormField>
      <ol className={styles.plainList}>
        {list.map((ins, i) => (
          <li key={i}>
            <InstructionEditor
              id={`${id}-${i}`}
              ins={ins}
              readOnly={readOnly}
              onChange={(next) => setList(list.map((x, k) => (k === i ? next : x)))}
              onMove={(by) => setList(moveItem(list, i, i + by))}
              onRemove={list.length > 1 ? () => setList(list.filter((_, k) => k !== i)) : undefined}
              first={i === 0}
              last={i === list.length - 1}
            />
          </li>
        ))}
      </ol>
      {readOnly ? null : (
        <div className={styles.actions}>
          <FormField id={`pi-add-${id}`} label="Add a step">
            {(control) => (
              <Select
                {...control}
                value=""
                onChange={(e) => {
                  if (e.target.value) setList([...list, newInstruction(e.target.value as InstructionCategory, list.length + 1)]);
                }}
              >
                <option value="">Choose a type of step</option>
                {CATEGORIES.map((c) => (
                  <option key={c} value={c}>
                    {CATEGORY_LABEL[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {canRemove ? (
            <Button size="sm" variant="ghost" onClick={onRemove}>
              Remove this phase
            </Button>
          ) : null}
        </div>
      )}
    </fieldset>
  );
}

function InstructionEditor({
  id,
  ins,
  readOnly,
  first,
  last,
  onChange,
  onMove,
  onRemove,
}: {
  id: string;
  ins: Instruction;
  readOnly: boolean;
  first: boolean;
  last: boolean;
  onChange(next: Instruction): void;
  onMove(by: number): void;
  onRemove?: () => void;
}) {
  const c = ins.characteristic;
  const hasCharacteristic = ins.category === "process-parameter-setpoint" || ins.category === "process-data-request" || ins.category === "inspection-characteristic";
  const setChar = (change: Partial<NonNullable<Instruction["characteristic"]>>) => onChange({ ...ins, characteristic: { name: "", type: "numeric", ...c, ...change } });
  const optionalNumber = (text: string): number | undefined => {
    const n = Number(text);
    return text.trim() === "" || !Number.isFinite(n) ? undefined : n;
  };
  return (
    <div className={styles.block}>
      <p className={styles.note}>{`Step ${ins.seq}: ${CATEGORY_LABEL[ins.category]}`}</p>
      <FormField id={`pi-text-${id}`} label={ins.category === "material-list" ? "Summary" : "Text"}>
        {(control) => <Textarea {...control} rows={2} maxLength={4000} value={ins.text ?? ""} disabled={readOnly} onChange={(e) => onChange({ ...ins, text: e.target.value })} />}
      </FormField>
      {hasCharacteristic ? (
        <div className={styles.premiumRow}>
          <FormField id={`pi-cname-${id}`} label="Characteristic">
            {(control) => <Input {...control} value={c?.name ?? ""} disabled={readOnly} maxLength={120} onChange={(e) => setChar({ name: e.target.value })} />}
          </FormField>
          <FormField id={`pi-ctype-${id}`} label="Kind of value">
            {(control) => (
              <Select {...control} value={c?.type ?? "numeric"} disabled={readOnly || ins.category === "process-parameter-setpoint"} onChange={(e) => setChar({ type: e.target.value as "numeric" | "text" | "boolean" })}>
                <option value="numeric">Number</option>
                <option value="text">Text</option>
                <option value="boolean">Yes or no</option>
              </Select>
            )}
          </FormField>
          {(c?.type ?? "numeric") === "numeric" ? (
            <>
              <FormField id={`pi-cunit-${id}`} label="Unit" optional>
                {(control) => <Input {...control} value={c?.unit ?? ""} disabled={readOnly} maxLength={20} onChange={(e) => setChar({ unit: e.target.value || undefined })} />}
              </FormField>
              <FormField id={`pi-cmin-${id}`} label="Lowest allowed" optional>
                {(control) => <Input {...control} inputMode="decimal" value={c?.min ?? ""} disabled={readOnly} onChange={(e) => setChar({ min: optionalNumber(e.target.value) })} />}
              </FormField>
              <FormField id={`pi-cmax-${id}`} label="Highest allowed" optional>
                {(control) => <Input {...control} inputMode="decimal" value={c?.max ?? ""} disabled={readOnly} onChange={(e) => setChar({ max: optionalNumber(e.target.value) })} />}
              </FormField>
            </>
          ) : null}
          {ins.category === "process-parameter-setpoint" ? (
            <FormField id={`pi-value-${id}`} label="Setpoint">
              {(control) => <Input {...control} inputMode="decimal" value={ins.value ?? ""} disabled={readOnly} onChange={(e) => onChange({ ...ins, value: optionalNumber(e.target.value) })} />}
            </FormField>
          ) : null}
        </div>
      ) : null}
      {readOnly ? null : (
        <div className={styles.actions}>
          <Button size="sm" variant="ghost" disabled={first} onClick={() => onMove(-1)}>
            Move up
          </Button>
          <Button size="sm" variant="ghost" disabled={last} onClick={() => onMove(1)}>
            Move down
          </Button>
          {onRemove ? (
            <Button size="sm" variant="ghost" onClick={onRemove}>
              Remove step
            </Button>
          ) : null}
        </div>
      )}
    </div>
  );
}
