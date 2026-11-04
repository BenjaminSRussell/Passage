import { describe, expect, it } from "vitest";
import { outcomeFromReplayStatus } from "./replayApply";

describe("outcomeFromReplayStatus", () => {
  it("applies 2xx", () => {
    expect(outcomeFromReplayStatus(200)).toEqual({ status: "APPLIED" });
    expect(outcomeFromReplayStatus(204)).toEqual({ status: "APPLIED" });
  });

  it("treats 409 as a conflict", () => {
    expect(outcomeFromReplayStatus(409)).toEqual({ status: "CONFLICT", reason: "conflict reported by the server" });
  });

  it.each([401, 408, 429, 500, 503])("retries %s as a transport error", (status) => {
    expect(() => outcomeFromReplayStatus(status)).toThrow("server unavailable");
  });

  it("rejects other client errors for good", () => {
    expect(outcomeFromReplayStatus(400)).toEqual({ status: "REJECTED", reason: "rejected with 400" });
    expect(outcomeFromReplayStatus(422)).toEqual({ status: "REJECTED", reason: "rejected with 422" });
  });
});
