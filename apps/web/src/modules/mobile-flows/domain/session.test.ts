import { describe, expect, it } from "vitest";
import { type DeviceSessionView, isAssignable, lockHolder, resume, sessionState, type WorkView } from "./session";

const T0 = 1_700_000_000_000;
const MIN = 60_000;

function session(over: Partial<DeviceSessionView> = {}): DeviceSessionView {
  return { sessionId: "s1", deviceId: "dev-1", userId: "alice", currentWorkId: "w1", lastSeenAt: T0, ...over };
}

describe("sessionState (FR-32.6)", () => {
  it.each([
    [0, "ACTIVE"],
    [29, "ACTIVE"],
    [30, "ACTIVE"],
    [31, "STALE"],
  ])("heartbeat %i min ago is %s", (minutesAgo, expected) => {
    expect(sessionState(session(), T0 + minutesAgo * MIN)).toBe(expected);
  });
});

describe("AC-32.8 stale session releases its work", () => {
  it("a session with no heartbeat for 31 minutes does not hold the lock, so its work is assignable", () => {
    const sessions = [session()];
    const now = T0 + 31 * MIN;
    const work: WorkView = { workId: "w1", status: "OPEN" };
    expect(lockHolder(sessions, "w1", now)).toBeNull();
    expect(isAssignable(work, sessions, now)).toBe(true);
  });

  it("an active session keeps the work locked", () => {
    const sessions = [session()];
    const now = T0 + 5 * MIN;
    expect(isAssignable({ workId: "w1", status: "OPEN" }, sessions, now)).toBe(false);
  });

  it("ASSIGNED work held by a stale session is assignable, by an active one it is not (BR-31.3)", () => {
    const sessions = [session()];
    const work: WorkView = { workId: "w1", status: "ASSIGNED" };
    expect(isAssignable(work, sessions, T0 + 31 * MIN)).toBe(true);
    expect(isAssignable(work, sessions, T0 + 10 * MIN)).toBe(false);
  });

  it("honours a warehouse idle timeout other than 30 minutes", () => {
    const sessions = [session()];
    const work: WorkView = { workId: "w1", status: "ASSIGNED" };
    expect(isAssignable(work, sessions, T0 + 11 * MIN, 10)).toBe(true);
    expect(isAssignable(work, sessions, T0 + 31 * MIN, 60)).toBe(false);
  });

  it("work past assignment is never assignable", () => {
    for (const status of ["IN_PROGRESS", "PARTIALLY_COMPLETED", "COMPLETED", "CANCELLED", "BLOCKED_NO_LOCATION"] as const) {
      expect(isAssignable({ workId: "w9", status }, [], T0)).toBe(false);
    }
  });
});

describe("resume after stale (BR-32.6)", () => {
  it.each<[string, WorkView | null, DeviceSessionView[], boolean]>([
    ["still OPEN and unlocked -> re-lock", { workId: "w1", status: "OPEN" }, [], true],
    ["still ASSIGNED before the sweep -> re-lock", { workId: "w1", status: "ASSIGNED" }, [], true],
    ["another user started it -> release", { workId: "w1", status: "IN_PROGRESS" }, [], false],
    ["work completed meanwhile -> release", { workId: "w1", status: "COMPLETED" }, [], false],
    ["work cancelled meanwhile -> release", { workId: "w1", status: "CANCELLED" }, [], false],
    ["work missing -> release", null, [], false],
    [
      "another active session took it -> release",
      { workId: "w1", status: "OPEN" },
      [session({ sessionId: "s2", userId: "bob", lastSeenAt: T0 + 40 * MIN })],
      false,
    ],
  ])("%s", (_name, work, others, relock) => {
    const me = session({ lastSeenAt: T0 });
    const now = T0 + 40 * MIN;
    const result = resume(me, work, [me, ...others], now);
    expect(result.relocked).toBe(relock);
  });
});
