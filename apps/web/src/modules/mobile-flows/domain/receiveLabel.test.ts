import { describe, expect, it } from "vitest";
import type { RunnerState } from "./flowRunner";
import { labelKey, labelOffer, lpLabelRequest, receivedSscc } from "./receiveLabel";

const SSCC = "340123456789012340";
const ITEM = "]C1" + "01" + "09501234567891" + "10LOT42" + "\u001D" + "17270630";

const state = (over: Partial<RunnerState> = {}): RunnerState => ({
  flowCode: "RECEIVE_PUTAWAY",
  stepId: null,
  values: { scan_lp: `]C100${SSCC}` },
  fields: { scan_lp: { "00": SSCC } },
  history: ["scan_lp", "scan_location", "confirm_putaway"],
  stepStartedAt: 0,
  ...over,
});

describe("label on receive", () => {
  it("offers a label for a received plate that carries an SSCC, and for nothing else", () => {
    expect(labelOffer(state())).toEqual({ sscc: SSCC });
    expect(labelOffer(state({ flowCode: "PICK" }))).toBeNull();
    expect(labelOffer(state({ fields: {} }))).toBeNull();
    expect(receivedSscc(state({ fields: { scan_lp: { "00": "123" } } }))).toBeNull();
  });

  it("builds the LP label job from the item's GS1 label, routed by document type", () => {
    const r = lpLabelRequest(SSCC, ITEM, { warehouseId: "w-1", zone: "DOCK" });
    expect(r).toEqual({
      ok: true,
      request: {
        templateCode: "LP_LABEL",
        documentType: "LP_LABEL",
        warehouseId: "w-1",
        zone: "DOCK",
        data: { lp: SSCC, item: "09501234567891", lot: "LOT42", expiry: "2027-06-30", sscc: SSCC },
      },
    });
  });

  it("says what is wrong with the item label", () => {
    expect(lpLabelRequest(SSCC, "ABC-123")).toEqual({ ok: false, reason: expect.stringContaining("not a GS1 item label") });
    const noLot = lpLabelRequest(SSCC, "]C10109501234567891");
    expect(noLot).toEqual({ ok: false, reason: "The item label has no lot or expiry date, so the label cannot be printed." });
    const badCheck = lpLabelRequest(SSCC, "]C10109501234567890" + "10LOT");
    expect(badCheck.ok).toBe(false);
  });

  it("keys the print by plate so a repeat returns the first job, and a retry by attempt", () => {
    expect(labelKey(SSCC)).toBe(`lp-label:${SSCC}`);
    expect(labelKey(SSCC, 2)).toBe(`lp-label:${SSCC}:2`);
  });
});
