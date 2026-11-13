import { describe, expect, it } from "vitest";
import type { Instruction, RecipeDocument } from "./api/client";
import { checkEntry, documentProblems, emptyDocument, flattenSteps, limitsText, messageFor, moveItem, newInstruction, parseNumber, renumber } from "./piSheet";

const temp: Instruction = { seq: 1, category: "process-data-request", text: "Record the temperature.", characteristic: { name: "Temperature", type: "numeric", unit: "degC", min: 6.5, max: 7.5 } };

function doc(instructions: Instruction[]): RecipeDocument {
  return { procedure: { name: "Batch", unitProcedures: [{ name: "Reactor", operations: [{ name: "Heat", phases: [{ name: "Ramp", instructions }, { name: "Hold", instructions: [{ seq: 1, category: "instruction-text", text: "Hold." }] }] }] }] } };
}

describe("designing", () => {
  it("starts from a document the service accepts", () => {
    expect(documentProblems(emptyDocument())).toEqual(["Unit / Operation / Phase, step 1: write the text."]);
  });

  it("numbers instructions in order after a move or a removal", () => {
    const list = renumber([newInstruction("instruction-text", 9), newInstruction("calculation", 4), newInstruction("signature-request", 2)]);
    expect(list.map((i) => i.seq)).toEqual([1, 2, 3]);
    expect(renumber(moveItem(list, 2, 0)).map((i) => i.category)).toEqual(["signature-request", "instruction-text", "calculation"]);
    expect(moveItem([1, 2], 0, 5)).toEqual([1, 2]);
  });

  it("names what is wrong in a sheet, in words", () => {
    const sheet = doc([
      { seq: 1, category: "instruction-text", text: " " },
      { seq: 2, category: "process-data-request", text: "x", characteristic: { name: "", type: "numeric" } },
      { seq: 3, category: "process-parameter-setpoint", value: 99, characteristic: { name: "Jacket", type: "numeric", min: 60, max: 90 } },
      { seq: 4, category: "inspection-characteristic", characteristic: { name: "pH", type: "numeric", min: 8, max: 7 } },
    ]);
    sheet.procedure.unitProcedures[0]!.operations[0]!.phases[1]!.name = "Ramp";
    expect(documentProblems(sheet)).toEqual([
      'Reactor / Heat, phases: the name "Ramp" is used twice.',
      "Reactor / Heat / Ramp, step 1: write the text.",
      "Reactor / Heat / Ramp, step 2: name the characteristic.",
      "Reactor / Heat / Ramp, step 3: the setpoint is outside its limits.",
      "Reactor / Heat / Ramp, step 4: the minimum is above the maximum.",
    ]);
  });
});

describe("running a sheet on the kiosk", () => {
  it("walks the steps in order and marks where a phase starts", () => {
    const steps = flattenSteps(doc([temp, { seq: 2, category: "instruction-text", text: "Next." }]));
    expect(steps.map((s) => [s.key, s.startsPhase])).toEqual([["Reactor/Heat/Ramp/1", true], ["Reactor/Heat/Ramp/2", false], ["Reactor/Heat/Hold/1", true]]);
    expect(steps[0]!.path).toBe("Batch/Reactor/Heat/Ramp");
  });

  it("takes a number, and a value outside its limits only with a comment (AC-25.12)", () => {
    expect(checkEntry(temp, { value: "", comment: "" })).toEqual({ ok: false, message: "Enter a number." });
    expect(checkEntry(temp, { value: "7,0", comment: "" })).toEqual({ ok: true, outOfLimit: false });
    expect(checkEntry(temp, { value: "7.8", comment: "" })).toEqual({ ok: false, message: "The value is outside its limits. Say why in the comment to go on." });
    expect(checkEntry(temp, { value: "7.8", comment: "probe was cold" })).toEqual({ ok: true, outOfLimit: true });
    expect(checkEntry({ ...temp, characteristic: { name: "Colour", type: "text" } }, { value: " ", comment: "" })).toEqual({ ok: false, message: "Enter a value." });
    expect(checkEntry({ ...temp, characteristic: { name: "Clean", type: "boolean" } }, { value: "true", comment: "" })).toEqual({ ok: true, outOfLimit: false });
    expect(checkEntry({ seq: 1, category: "signature-request", text: "Sign." }, { value: "", comment: "" })).toEqual({ ok: false, message: "Type your name to sign." });
    expect(checkEntry({ seq: 1, category: "instruction-text", text: "Read." }, { value: "", comment: "" })).toEqual({ ok: true, outOfLimit: false });
  });

  it("parses numbers the way an operator types them", () => {
    expect([parseNumber("7.5"), parseNumber(" 7,5 "), parseNumber("-.5"), parseNumber("7.5.1"), parseNumber("abc"), parseNumber("")]).toEqual([7.5, 7.5, -0.5, null, null, null]);
  });

  it("records what a step collected as a process message with its limits and its comment", () => {
    const [step] = flattenSteps(doc([temp]));
    expect(messageFor("rec-1", step!, { value: "7.8", comment: "probe was cold" })).toEqual({
      sourceMessageId: "rec-1:Reactor/Heat/Ramp/1",
      categoryCode: "process-data",
      phasePath: "Batch/Reactor/Heat/Ramp",
      values: [
        { name: "Temperature", type: "numeric", value: "7.8", min: "6.5", max: "7.5", unit: "degC" },
        { name: "comment", type: "text", value: "probe was cold" },
      ],
    });
    const insp = flattenSteps(doc([{ ...temp, category: "inspection-characteristic" }]))[0]!;
    expect(messageFor("rec-1", insp, { value: "7", comment: "" })?.categoryCode).toBe("quality-result");
    const set = flattenSteps(doc([{ seq: 1, category: "process-parameter-setpoint", value: 72, characteristic: { name: "Jacket", type: "numeric", unit: "degC", min: 60, max: 90 } }]))[0]!;
    expect(messageFor("rec-1", set, undefined)?.values[0]).toEqual({ name: "Jacket", type: "numeric", value: "72", min: "60", max: "90", unit: "degC" });
    const sign = flattenSteps(doc([{ seq: 1, category: "signature-request", text: "Sign" }]))[0]!;
    expect(messageFor("rec-1", sign, { value: " Ana ", comment: "confirmed" })?.values).toEqual([
      { name: "signed-by", type: "text", value: "Ana" },
      { name: "signature-meaning", type: "text", value: "performed" },
    ]);
    expect(checkEntry({ seq: 1, category: "signature-request", text: "Sign.", meaning: "approved" }, { value: "Ana", comment: "" })).toEqual({
      ok: false,
      message: 'Confirm the statement "I approve this step." to sign.',
    });
    expect(messageFor("rec-1", flattenSteps(doc([{ seq: 1, category: "instruction-text", text: "Read" }]))[0]!, undefined)).toBeNull();
  });

  it("says the limits in words", () => {
    expect([limitsText(temp.characteristic), limitsText({ min: 3, unit: "bar" }), limitsText({ max: 9 }), limitsText({ unit: "x" })]).toEqual(["6.5 to 7.5 degC", "at least 3 bar", "at most 9", ""]);
  });
});
