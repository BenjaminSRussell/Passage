import { afterEach, describe, expect, it, vi } from "vitest";
import { serverClient } from "./client";
import { OfflineError, RefusedError } from "../domain/sync";

const action = { id: "00000000-0000-4000-8000-000000000001", kind: "NOTE", deviceTime: "2027-01-01T10:00:00Z", text: "x" } as never;

describe("technician API client", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("keeps the queue when the session has ended (401 is not a refusal)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "AUTH-001", detail: "signed out" }), { status: 401 })));
    await expect(serverClient.postActions("wo", "tech", [action])).rejects.toBeInstanceOf(OfflineError);
  });

  it("treats any other 4xx as a refusal", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "SVC-SYNC-003", detail: "not yours" }), { status: 403 })));
    await expect(serverClient.postActions("wo", "tech", [action])).rejects.toBeInstanceOf(RefusedError);
  });
});
