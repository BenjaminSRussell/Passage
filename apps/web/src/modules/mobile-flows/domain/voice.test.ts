import { describe, expect, it } from "vitest";
import type { FlowStep } from "./flowGraph";
import { parseUtterance, promptFor, routeUtterance, spokenNumber } from "./voice";

const scan: FlowStep = { id: "loc", type: "scan", prompt: "Go to aisle 3.", expectedScan: ["LOCATION"], next: { default: "qty" } };
const input: FlowStep = { id: "qty", type: "input", prompt: "Pick how many?", next: { default: "ok" } };
const confirm: FlowStep = { id: "ok", type: "confirm", prompt: "Pick complete.", next: { default: "end" } };
const branch: FlowStep = { id: "b", type: "branch", prompt: "Enough stock?", next: { short: "s", skip: "k", ready: "r" } };

describe("spoken numbers", () => {
  it("reads digit words, number words and digits", () => {
    expect(spokenNumber("four oh two")).toBe("402");
    expect(spokenNumber("niner niner")).toBe("99");
    expect(spokenNumber("12")).toBe("12");
    expect(spokenNumber("twelve")).toBe("12");
    expect(spokenNumber("forty five")).toBe("45");
    expect(spokenNumber("one hundred and five")).toBe("105");
    expect(spokenNumber("twenty")).toBe("20");
  });

  it("refuses what is not a number", () => {
    for (const bad of ["", "banana", "forty banana", "hundred"]) expect(spokenNumber(bad), bad).toBeNull();
  });
});

describe("grammar", () => {
  it("parses commands and quantities", () => {
    expect(parseUtterance("Ready.")).toEqual({ kind: "confirm" });
    expect(parseUtterance("go back")).toEqual({ kind: "back" });
    expect(parseUtterance("Say again")).toEqual({ kind: "repeat" });
    expect(parseUtterance("short pick")).toEqual({ kind: "outcome", word: "short" });
    expect(parseUtterance("quantity twelve")).toEqual({ kind: "value", value: "12" });
    expect(parseUtterance("zero seven")).toEqual({ kind: "value", value: "07" });
    expect(parseUtterance("blah")).toEqual({ kind: "unknown", heard: "blah" });
  });
});

describe("routing to the focused step", () => {
  it("answers a scan step with spoken check digits and an input step with a quantity", () => {
    expect(routeUtterance("four two", scan)).toEqual({ do: "scan", raw: "42" });
    expect(routeUtterance("twelve", input)).toEqual({ do: "value", value: "12" });
  });

  it("confirms on ready, but only where a confirmation is expected", () => {
    expect(routeUtterance("ready", confirm)).toEqual({ do: "confirm" });
    expect(routeUtterance("ready", input)).toEqual({ do: "say", text: "Say the value first." });
    expect(routeUtterance("ready", branch)).toEqual({ do: "outcome", value: "ready" });
  });

  it("maps short and skip to the branch outcome of that name", () => {
    expect(routeUtterance("short", branch)).toEqual({ do: "outcome", value: "short" });
    expect(routeUtterance("skip", input)).toEqual({ do: "say", text: "Not available on this step." });
  });

  it("goes back, repeats the prompt, asks again, and has nothing to say without a step", () => {
    expect(routeUtterance("back", input)).toEqual({ do: "back" });
    expect(routeUtterance("repeat", scan)).toEqual({ do: "say", text: "Go to aisle 3." });
    expect(routeUtterance("mumble", input)).toEqual({ do: "say", text: "Say again." });
    expect(routeUtterance("ready", undefined)).toEqual({ do: "say", text: "Nothing is waiting for an answer." });
  });

  it("reads out what the worker can say", () => {
    expect(promptFor(branch)).toBe("Enough stock? Say short, skip, ready.");
    expect(promptFor(confirm)).toBe("Pick complete. Say ready.");
    expect(promptFor(scan)).toBe("Go to aisle 3.");
  });
});
