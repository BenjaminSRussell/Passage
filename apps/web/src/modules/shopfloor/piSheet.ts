// The PI sheet designer's and the kiosk runner's rules, apart from any screen (FR-25.16). A PI sheet is the ISA-88
// structure of a control recipe: unit procedures, operations, phases and, in each phase, numbered process instructions.

import type { Instruction, InstructionCategory, RecipeDocument, SignatureMeaning } from "./api/client";

/** Signature meanings (21 CFR 11.50): what the signer states by signing the step. */
export const MEANING_LABEL: Record<SignatureMeaning, string> = {
  performed: "Performed by",
  verified: "Verified by",
  reviewed: "Reviewed by",
  approved: "Approved by",
};

/** The sentence a signer confirms on the kiosk before the signature is recorded. */
export function meaningStatement(meaning: SignatureMeaning | undefined): string {
  switch (meaning ?? "performed") {
    case "verified":
      return "I verified this step.";
    case "reviewed":
      return "I reviewed this step.";
    case "approved":
      return "I approve this step.";
    default:
      return "I performed this step.";
  }
}

export const CATEGORY_LABEL: Record<InstructionCategory, string> = {
  "instruction-text": "Instruction",
  "process-parameter-setpoint": "Setpoint",
  "process-data-request": "Enter a value",
  "calculation": "Calculation",
  "inspection-characteristic": "Inspection",
  "material-list": "Material list",
  "signature-request": "Signature",
  "sequence-definition": "Sequence",
};

export const CATEGORIES = Object.keys(CATEGORY_LABEL) as InstructionCategory[];

export function emptyDocument(name = "New PI sheet"): RecipeDocument {
  return {
    procedure: {
      name,
      unitProcedures: [{ name: "Unit", operations: [{ name: "Operation", phases: [{ name: "Phase", instructions: [newInstruction("instruction-text", 1)] }] }] }],
    },
  };
}

export function newInstruction(category: InstructionCategory, seq: number): Instruction {
  switch (category) {
    case "process-parameter-setpoint":
      return { seq, category, text: "", value: 0, characteristic: { name: "", type: "numeric" } };
    case "process-data-request":
    case "inspection-characteristic":
      return { seq, category, text: "", characteristic: { name: "", type: "numeric" } };
    case "material-list":
      return { seq, category, text: "Charge the materials.", materials: [] };
    default:
      return { seq, category, text: "" };
  }
}

/** Instructions are numbered 1, 2, 3 in order within their phase; call after any insert, remove or move. */
export function renumber(instructions: readonly Instruction[]): Instruction[] {
  return instructions.map((ins, i) => ({ ...ins, seq: i + 1 }));
}

export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  if (to < 0 || to >= list.length || from === to) return [...list];
  const copy = [...list];
  const [item] = copy.splice(from, 1);
  copy.splice(to, 0, item as T);
  return copy;
}

/** A person-readable path of one phase, as process messages name it. */
export function phasePath(doc: RecipeDocument, up: string, op: string, phase: string): string {
  return [doc.procedure.name, up, op, phase].join("/");
}

/** Problems a document has, keyed by a readable place, so the designer can show them before the service does. */
export function documentProblems(doc: RecipeDocument): string[] {
  const out: string[] = [];
  const unique = (names: string[], where: string) => {
    const seen = new Set<string>();
    for (const n of names) {
      const t = n.trim();
      if (t === "" || t.length > 120) out.push(`${where}: a name must be 1 to 120 characters.`);
      else if (seen.has(t)) out.push(`${where}: the name "${t}" is used twice.`);
      seen.add(t);
    }
  };
  if (doc.procedure.name.trim() === "") out.push("The procedure needs a name.");
  if (doc.procedure.unitProcedures.length === 0) out.push("The procedure needs at least one unit procedure.");
  unique(doc.procedure.unitProcedures.map((u) => u.name), "Unit procedures");
  for (const up of doc.procedure.unitProcedures) {
    if (up.operations.length === 0) out.push(`${up.name}: add an operation.`);
    unique(up.operations.map((o) => o.name), `${up.name}, operations`);
    for (const op of up.operations) {
      if (op.phases.length === 0) out.push(`${up.name} / ${op.name}: add a phase.`);
      unique(op.phases.map((p) => p.name), `${up.name} / ${op.name}, phases`);
      for (const ph of op.phases) {
        const where = `${up.name} / ${op.name} / ${ph.name}`;
        if (ph.instructions.length === 0) out.push(`${where}: add an instruction.`);
        for (const ins of ph.instructions) {
          const step = `${where}, step ${ins.seq}`;
          if (["instruction-text", "signature-request", "material-list", "sequence-definition", "calculation"].includes(ins.category) && (ins.text ?? "").trim() === "") {
            out.push(`${step}: write the text.`);
          }
          if (["process-parameter-setpoint", "process-data-request", "inspection-characteristic"].includes(ins.category)) {
            const c = ins.characteristic;
            if (!c || c.name.trim() === "") out.push(`${step}: name the characteristic.`);
            if (c?.min !== undefined && c.max !== undefined && c.min > c.max) out.push(`${step}: the minimum is above the maximum.`);
            if (ins.category === "process-parameter-setpoint" && ins.value !== undefined && c) {
              if ((c.min !== undefined && ins.value < c.min) || (c.max !== undefined && ins.value > c.max)) out.push(`${step}: the setpoint is outside its limits.`);
            }
          }
        }
      }
    }
  }
  return out;
}

// The kiosk runner.

/** One step the operator works through, with where it sits in the recipe. */
export interface RunStep {
  /** Stable within a recipe: unit/operation/phase/seq. Used as the process message's source id. */
  key: string;
  path: string;
  unit: string;
  operation: string;
  phase: string;
  instruction: Instruction;
  /** First step of its phase: the runner shows the phase heading. */
  startsPhase: boolean;
}

export function flattenSteps(doc: RecipeDocument): RunStep[] {
  const steps: RunStep[] = [];
  for (const up of doc.procedure.unitProcedures) {
    for (const op of up.operations) {
      for (const ph of op.phases) {
        ph.instructions.forEach((instruction, i) => {
          steps.push({
            key: [up.name, op.name, ph.name, instruction.seq].join("/"),
            path: phasePath(doc, up.name, op.name, ph.name),
            unit: up.name,
            operation: op.name,
            phase: ph.name,
            instruction,
            startsPhase: i === 0,
          });
        });
      }
    }
  }
  return steps;
}

/** The value a step collects: the entry a person makes. Categories that only ask for an acknowledgement collect none. */
export function collectsValue(ins: Instruction): boolean {
  return ins.category === "process-data-request" || ins.category === "inspection-characteristic" || ins.category === "signature-request";
}

export interface StepEntry {
  value: string;
  comment: string;
}

export function parseNumber(text: string): number | null {
  const t = text.trim().replace(",", ".");
  if (t === "" || !/^[-+]?(\d+(\.\d*)?|\.\d+)$/.test(t)) return null;
  return Number(t);
}

export type EntryCheck = { ok: true; outOfLimit: boolean } | { ok: false; message: string };

/** Whether a step can be completed with this entry. A value outside its limits needs a comment (BR-25.12). */
export function checkEntry(ins: Instruction, entry: StepEntry): EntryCheck {
  if (ins.category === "signature-request") {
    if (entry.value.trim() === "") return { ok: false, message: "Type your name to sign." };
    return entry.comment === "confirmed" ? { ok: true, outOfLimit: false } : { ok: false, message: `Confirm the statement "${meaningStatement(ins.meaning)}" to sign.` };
  }
  if (ins.category !== "process-data-request" && ins.category !== "inspection-characteristic") return { ok: true, outOfLimit: false };
  const c = ins.characteristic;
  if (c?.type === "text") return entry.value.trim() === "" ? { ok: false, message: "Enter a value." } : { ok: true, outOfLimit: false };
  if (c?.type === "boolean") return entry.value === "true" || entry.value === "false" ? { ok: true, outOfLimit: false } : { ok: false, message: "Choose yes or no." };
  const n = parseNumber(entry.value);
  if (n === null) return { ok: false, message: "Enter a number." };
  const out = (c?.min !== undefined && n < c.min) || (c?.max !== undefined && n > c.max);
  if (out && entry.comment.trim() === "") return { ok: false, message: "The value is outside its limits. Say why in the comment to go on." };
  return { ok: true, outOfLimit: out };
}

/** The limits of a characteristic as text, for the label. */
export function limitsText(c: { min?: number; max?: number; unit?: string } | undefined): string {
  if (!c || (c.min === undefined && c.max === undefined)) return "";
  const unit = c.unit ? ` ${c.unit}` : "";
  if (c.min !== undefined && c.max !== undefined) return `${c.min} to ${c.max}${unit}`;
  return c.min !== undefined ? `at least ${c.min}${unit}` : `at most ${c.max}${unit}`;
}

export interface ProcessMessageDraft {
  sourceMessageId: string;
  categoryCode: "process-data" | "quality-result";
  phasePath: string;
  values: { name: string; type: "numeric" | "text" | "boolean"; value: string; min?: string; max?: string; unit?: string }[];
}

/**
 * The process message a finished step records, or null for a step that records nothing. A setpoint the operator
 * confirmed is recorded as the value set; entries are recorded with their limits, so the service flags what is
 * outside them, and a comment travels as its own text value. Idempotent by recipe and step.
 */
export function messageFor(recipeId: string, step: RunStep, entry: StepEntry | undefined): ProcessMessageDraft | null {
  const ins = step.instruction;
  const c = ins.characteristic;
  const base = { sourceMessageId: `${recipeId}:${step.key}`, phasePath: step.path };
  const num = (n: number | undefined) => (n === undefined ? undefined : String(n));
  if (ins.category === "process-parameter-setpoint" && c) {
    return { ...base, categoryCode: "process-data", values: [{ name: c.name, type: "numeric", value: String(ins.value ?? 0), min: num(c.min), max: num(c.max), unit: c.unit }] };
  }
  if ((ins.category === "process-data-request" || ins.category === "inspection-characteristic") && c && entry) {
    const values: ProcessMessageDraft["values"] = [
      c.type === "numeric"
        ? { name: c.name, type: "numeric", value: String(parseNumber(entry.value) ?? entry.value), min: num(c.min), max: num(c.max), unit: c.unit }
        : { name: c.name, type: c.type, value: entry.value },
    ];
    if (entry.comment.trim() !== "") values.push({ name: "comment", type: "text", value: entry.comment.trim() });
    return { ...base, categoryCode: ins.category === "inspection-characteristic" ? "quality-result" : "process-data", values };
  }
  if (ins.category === "signature-request" && entry && entry.value.trim() !== "") {
    return {
      ...base,
      categoryCode: "process-data",
      values: [
        { name: "signed-by", type: "text", value: entry.value.trim() },
        { name: "signature-meaning", type: "text", value: ins.meaning ?? "performed" },
      ],
    };
  }
  return null;
}
