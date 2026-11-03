import { describe, expect, it, vi } from "vitest";
import { buildActivities } from "../src/activityRegistry.js";

const seed = Buffer.alloc(32, 7).toString("base64");

describe("worker activity registry", () => {
  it("registers nothing and warns instead of crashing when no signing key is configured", () => {
    const warn = vi.fn();
    const activities = buildActivities({}, () => "", warn);
    expect(Object.keys(activities)).toEqual([]);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("no activities are registered"), { reason: expect.stringContaining("required") });
  });

  it("warns and registers nothing when the key is malformed", () => {
    const warn = vi.fn();
    const activities = buildActivities({ PASSAGE_SERVICE_SIGNING_KEY: "AAAA" }, () => "", warn);
    expect(Object.keys(activities)).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("registers the payment run, ECO and CAPA and tender activities, and never the party decision, when a key is present", () => {
    const warn = vi.fn();
    const activities = buildActivities({ PASSAGE_SERVICE_SIGNING_KEY: seed }, () => "", warn);
    expect(Object.keys(activities).sort()).toEqual(
      [
        "approveEco",
        "cancelEco",
        "checkEffectiveness",
        "completePaymentRun",
        "completeStep",
        "failPaymentRun",
        "generatePaymentFile",
        "getTender",
        "postPaymentJournal",
        "rejectEco",
        "releaseEco",
        "settlePaymentRun",
        "startCorrection",
        "startPaymentRun",
        "startTender",
        "sweepExpiredTenders",
      ],
    );
    expect(activities).not.toHaveProperty("decidePartyChange");
    expect(warn).not.toHaveBeenCalled();
  });

  it("reads the key from a mounted file", () => {
    const activities = buildActivities({ PASSAGE_SERVICE_SIGNING_KEY_FILE: "/secret/key" }, (path) => (path === "/secret/key" ? `${seed}\n` : ""), vi.fn());
    expect(activities).toHaveProperty("startPaymentRun");
  });
});
