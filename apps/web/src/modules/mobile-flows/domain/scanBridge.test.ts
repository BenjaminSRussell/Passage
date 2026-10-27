import { describe, expect, it } from "vitest";
import { RECEIVE_PUTAWAY_FLOW } from "./coreFlows";
import { startFlow, submitStep } from "./flowRunner";
import { INTENT_DATA_KEYS, MAX_SCAN_LENGTH, SCAN_EVENT, listenForScans, parseIntent } from "./scanBridge";

describe("parseIntent", () => {
  it("reads the DataWedge data string and keeps the symbology prefix", () => {
    const r = parseIntent({ source: "DATAWEDGE", extras: { [INTENT_DATA_KEYS.DATAWEDGE]: "]C1" + "0100950110102091710261231" } });
    expect(r).toEqual({ ok: true, raw: "]C10100950110102091710261231", source: "DATAWEDGE" });
  });

  it("reads the Honeywell data extra", () => {
    const r = parseIntent({ source: "HONEYWELL_DC", extras: { [INTENT_DATA_KEYS.HONEYWELL_DC]: "LP0001" } });
    expect(r).toEqual({ ok: true, raw: "LP0001", source: "HONEYWELL_DC" });
  });

  it("accepts keyboard wedge text directly", () => {
    expect(parseIntent({ source: "KEYBOARD_WEDGE", raw: "A-01-02" })).toEqual({ ok: true, raw: "A-01-02", source: "KEYBOARD_WEDGE" });
  });

  it.each([
    ["not an object", "x"],
    ["unknown source", { source: "CAMERA", raw: "x" }],
    ["missing extras", { source: "DATAWEDGE" }],
    ["empty data", { source: "DATAWEDGE", extras: { [INTENT_DATA_KEYS.DATAWEDGE]: "" } }],
    ["data not a string", { source: "HONEYWELL_DC", extras: { [INTENT_DATA_KEYS.HONEYWELL_DC]: 7 } }],
    ["oversized data", { source: "KEYBOARD_WEDGE", raw: "x".repeat(MAX_SCAN_LENGTH + 1) }],
  ])("rejects %s", (_name, detail) => {
    expect(parseIntent(detail).ok).toBe(false);
  });

  it("feeds the scan rule: a DataWedge ]C1 intent on a confirm step is ignored (AC-32.7)", () => {
    const started = startFlow(RECEIVE_PUTAWAY_FLOW, 0);
    if (!started.ok) throw new Error("core flow must start");
    const onConfirm = { ...started.state, stepId: "confirm_putaway" };
    const intent = parseIntent({ source: "DATAWEDGE", extras: { [INTENT_DATA_KEYS.DATAWEDGE]: "]C10100950110102091" } });
    if (!intent.ok) throw new Error(intent.reason);
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, onConfirm, { kind: "scan", raw: intent.raw, source: intent.source }, { deviceId: "d", userId: "u" }, 10);
    expect(r).toMatchObject({ ok: false, ignored: true });
    expect(r.state.stepId).toBe("confirm_putaway");
  });
});

describe("listenForScans", () => {
  it("delivers forwarded intents until unsubscribed", () => {
    const target = new EventTarget();
    const seen: string[] = [];
    const stop = listenForScans(target, (r) => {
      if (r.ok) seen.push(r.raw);
    });
    const fire = (detail: unknown) => target.dispatchEvent(Object.assign(new Event(SCAN_EVENT), { detail }));
    fire({ source: "KEYBOARD_WEDGE", raw: "LP0001" });
    stop();
    fire({ source: "KEYBOARD_WEDGE", raw: "LP0002" });
    expect(seen).toEqual(["LP0001"]);
  });
});
