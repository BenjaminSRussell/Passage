import { describe, expect, it } from "vitest";
import { listenForRfid, MAX_READS_PER_ROUND, parseRound, RFID_EVENT, RfidAggregator } from "./rfid";

const SGTIN = "3074257BF7194E4000001A85";
const SSCC = "3174257BF4499602D2000000";

describe("RFID rounds", () => {
  it("validates a forwarded round", () => {
    expect(parseRound({ reads: [{ epc: "3074257bf7194e4000001a85 ", rssi: -48, antenna: 2 }] })).toEqual({
      ok: true,
      reads: [{ epc: SGTIN, rssi: -48, antenna: 2 }],
    });
    for (const bad of [null, "x", {}, { reads: [{}] }, { reads: [null] }, { reads: new Array(MAX_READS_PER_ROUND + 1).fill({ epc: SGTIN }) }]) {
      expect(parseRound(bad).ok, JSON.stringify(bad)?.slice(0, 40)).toBe(false);
    }
  });

  it("scans each tag once, strongest first, and reports unreadable tags", () => {
    let t = 1000;
    const agg = new RfidAggregator(5000, () => t);
    const out = agg.accept([
      { epc: SGTIN, rssi: -70 },
      { epc: SSCC, rssi: -50 },
      { epc: SGTIN, rssi: -40 },
      { epc: "E280689400004012345ABCDE" },
    ]);
    expect(out.scans.map((s) => [s.epc, s.raw, s.rssi])).toEqual([
      [SGTIN, "]C10180614141123458216789", -40],
      [SSCC, "]C100106141412345678908", -50],
    ]);
    expect(out.unreadable).toHaveLength(1);
    expect(out.unreadable[0].reason).toContain("header");

    // The same tags in the next round, still in the field: nothing new.
    t += 1000;
    expect(agg.accept([{ epc: SGTIN }, { epc: SSCC }]).scans).toEqual([]);
    // After the window they count again.
    t += 5000;
    expect(agg.accept([{ epc: SGTIN }]).scans).toHaveLength(1);
  });

  it("listens for the shell's passage:rfid events", () => {
    const target = new EventTarget();
    const got: unknown[] = [];
    const stop = listenForRfid(target, (r) => got.push(r));
    target.dispatchEvent(new CustomEvent(RFID_EVENT, { detail: { reads: [{ epc: SGTIN }] } }));
    stop();
    target.dispatchEvent(new CustomEvent(RFID_EVENT, { detail: { reads: [{ epc: SGTIN }] } }));
    expect(got).toEqual([{ ok: true, reads: [{ epc: SGTIN }] }]);
  });
});
