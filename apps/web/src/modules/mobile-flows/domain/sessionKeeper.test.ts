import { describe, expect, it, vi } from "vitest";
import { SessionKeeper, type KeeperClient, type KeeperState } from "./sessionKeeper";

function setup(overrides: Partial<KeeperClient> = {}) {
  const calls: string[] = [];
  const client: KeeperClient = {
    start: vi.fn(async () => {
      calls.push("start");
      return { session: { sessionId: "s1" } };
    }),
    heartbeat: vi.fn(async (id: string) => {
      calls.push(`beat ${id}`);
      return { relocked: true };
    }),
    end: vi.fn(async (id: string) => {
      calls.push(`end ${id}`);
    }),
    ...overrides,
  };
  const states: KeeperState[] = [];
  const keeper = new SessionKeeper(client, (s) => states.push(s), () => new Date("2026-11-10T10:00:00Z"));
  return { keeper, client, calls, states };
}

describe("SessionKeeper", () => {
  it("starts a session, then beats it with the device clock, then signs out", async () => {
    const { keeper, client, calls } = setup();
    expect((await keeper.start()).status).toBe("active");
    expect(keeper.sessionId).toBe("s1");
    await keeper.beat();
    expect(client.heartbeat).toHaveBeenCalledWith("s1", new Date("2026-11-10T10:00:00Z"));
    await keeper.stop();
    expect(calls).toEqual(["start", "beat s1", "end s1"]);
    expect(keeper.state.status).toBe("idle");
  });

  it("starts on the first beat when no session is open", async () => {
    const { keeper, calls } = setup();
    await keeper.beat();
    expect(calls).toEqual(["start"]);
  });

  it("shows the resume notice of a run that was in progress", async () => {
    const { keeper } = setup({ start: async () => ({ session: { sessionId: "s1" }, notice: { message: "Session resumed on flow version 3." } }) });
    expect((await keeper.start()).message).toBe("Session resumed on flow version 3.");
  });

  it("is blocked, not offline, when another worker holds the device", async () => {
    const { keeper } = setup({ start: async () => Promise.reject({ status: 409, code: "MOB-SESSION-ACTIVE" }) });
    const state = await keeper.start();
    expect(state.status).toBe("blocked");
    expect(state.message).toContain("Another worker");
  });

  it("goes offline on any other failure, keeps the session id and recovers on the next beat", async () => {
    let down = true;
    const { keeper } = setup({
      heartbeat: async () => {
        if (down) throw new Error("network");
        return { relocked: true };
      },
    });
    await keeper.start();
    expect((await keeper.beat()).status).toBe("offline");
    expect(keeper.sessionId).toBe("s1");
    down = false;
    expect((await keeper.beat()).status).toBe("active");
  });

  it("is offline, not blocked, when the service is not deployed", async () => {
    const { keeper } = setup({ start: async () => Promise.reject({ status: 404 }) });
    expect((await keeper.start()).status).toBe("offline");
    expect(keeper.sessionId).toBeUndefined();
  });

  it("starts again when the server ended the session (a takeover or a supervisor sign-out)", async () => {
    let first = true;
    const { keeper, calls } = setup({
      heartbeat: async () => {
        if (first) {
          first = false;
          throw { status: 409, code: "MOB-SESSION-ENDED" };
        }
        return { relocked: true };
      },
    });
    await keeper.start();
    expect((await keeper.beat()).status).toBe("active");
    expect(calls).toEqual(["start", "start"]);
  });

  it("reports a lost work lock", async () => {
    const { keeper } = setup({ heartbeat: async () => ({ relocked: false, reason: "The work was reassigned." }) });
    await keeper.start();
    expect((await keeper.beat()).message).toBe("The work was reassigned.");
  });

  it("signs out even when the end call fails", async () => {
    const { keeper } = setup({ end: async () => Promise.reject(new Error("down")) });
    await keeper.start();
    await expect(keeper.stop()).resolves.toBeUndefined();
    expect(keeper.state.status).toBe("idle");
  });
});
