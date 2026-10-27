import { describe, expect, it } from "vitest";
import {
  CODE_OVERRIDE_EXPIRED,
  consumeOverride,
  issueOverride,
  logException,
  OVERRIDE_WINDOW_MS,
  validateException,
  type ExceptionInput,
  type OverrideGrant,
} from "./exceptions";

const REASONS = ["DAMAGED_CARTON", "NO_STOCK_FOUND"];
const base: ExceptionInput = {
  kind: "SKIP",
  userId: "worker-1",
  deviceId: "TC52-0042",
  locationCode: "A1-01-02",
  workLineId: "line-42",
};

describe("validateException", () => {
  it.each<[string, ExceptionInput, boolean]>([
    ["skip needs no extra fields", base, true],
    ["short pick needs a whole positive quantity", { ...base, kind: "SHORT_PICK", shortQuantity: 2 }, true],
    ["short pick zero rejected", { ...base, kind: "SHORT_PICK", shortQuantity: 0 }, false],
    ["short pick fractional rejected", { ...base, kind: "SHORT_PICK", shortQuantity: 1.5 }, false],
    ["short pick missing quantity rejected", { ...base, kind: "SHORT_PICK" }, false],
    ["damage needs a reason", { ...base, kind: "DAMAGED", reason: "  " }, false],
    ["damage with reason", { ...base, kind: "DAMAGED", reason: "crushed corner" }, true],
    ["override needs a configured reason", { ...base, kind: "OVERRIDE", reason: "because" }, false],
    ["override with configured reason", { ...base, kind: "OVERRIDE", reason: "NO_STOCK_FOUND" }, true],
    ["missing device is rejected", { ...base, deviceId: "" }, false],
    ["missing location is rejected", { ...base, locationCode: "" }, false],
  ])("%s", (_name, input, ok) => {
    expect(validateException(input, REASONS).ok).toBe(ok);
  });
});

describe("logException", () => {
  it("appends a logged entry with user, device, location and time, without mutating the input log", () => {
    const before = [] as const;
    const res = logException(before, base, "exc-1", 1_700_000_000_000, REASONS);
    expect(res.error).toBeUndefined();
    expect(res.log).toHaveLength(1);
    expect(res.entry).toMatchObject({ id: "exc-1", userId: "worker-1", deviceId: "TC52-0042", locationCode: "A1-01-02", occurredAt: 1_700_000_000_000 });
    expect(before).toHaveLength(0);
  });

  it("returns an error and leaves the log unchanged when invalid", () => {
    const res = logException([], { ...base, kind: "DAMAGED" }, "exc-2", 1, REASONS);
    expect(res.error).toBe("Damage needs a reason.");
    expect(res.log).toHaveLength(0);
  });
});

describe("supervisor override (BR-32.7)", () => {
  const T0 = 1_700_000_000_000;

  it("a supervisor cannot override their own work", () => {
    expect(issueOverride("g1", "sup-1", "sup-1", "SKIP:line-42", T0).error).toMatch(/own work/);
  });

  it("a grant authorizes exactly one action for the named worker", () => {
    const { grant } = issueOverride("g1", "sup-1", "worker-1", "SKIP:line-42", T0);
    if (!grant) throw new Error("expected a grant");
    const first = consumeOverride(grant, "worker-1", "SKIP:line-42", T0 + 1_000);
    expect(first.grant?.usedAt).toBe(T0 + 1_000);
    // Reuse of the same grant is refused with MOB-0110.
    const again = consumeOverride(first.grant as OverrideGrant, "worker-1", "SKIP:line-42", T0 + 2_000);
    expect(again.code).toBe(CODE_OVERRIDE_EXPIRED);
  });

  it("the window closes after 60 s, inclusive at 60 s", () => {
    const { grant } = issueOverride("g1", "sup-1", "worker-1", "SKIP:line-42", T0);
    if (!grant) throw new Error("expected a grant");
    expect(consumeOverride(grant, "worker-1", "SKIP:line-42", T0 + OVERRIDE_WINDOW_MS).grant).toBeDefined();
    const late = consumeOverride(grant, "worker-1", "SKIP:line-42", T0 + OVERRIDE_WINDOW_MS + 1);
    expect(late.code).toBe(CODE_OVERRIDE_EXPIRED);
  });

  it("is refused for another worker or another action, without spending the grant", () => {
    const { grant } = issueOverride("g1", "sup-1", "worker-1", "SKIP:line-42", T0);
    if (!grant) throw new Error("expected a grant");
    expect(consumeOverride(grant, "worker-2", "SKIP:line-42", T0).error).toMatch(/another worker/);
    expect(consumeOverride(grant, "worker-1", "SHORT_PICK:line-42", T0).error).toMatch(/another action/);
    expect(grant.usedAt).toBeNull();
  });

  it("a grant issued in the future is treated as invalid", () => {
    const { grant } = issueOverride("g1", "sup-1", "worker-1", "SKIP:line-42", T0);
    if (!grant) throw new Error("expected a grant");
    expect(consumeOverride(grant, "worker-1", "SKIP:line-42", T0 - 1).code).toBe(CODE_OVERRIDE_EXPIRED);
  });
});
