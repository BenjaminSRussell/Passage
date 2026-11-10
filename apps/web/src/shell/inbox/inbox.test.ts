import { describe, expect, it, vi } from "vitest";
import { decideInboxItem, type InboxItem } from "./inbox";

const item = (source: string): InboxItem => ({ id: "c1", source, kind: "K", reason: "r", number: "n", link: "/x", actions: ["approve"], createdAt: "2026-11-01T00:00:00Z" });

describe("decideInboxItem", () => {
  it("posts a party decision to the pending-change endpoint", async () => {
    const fetchImpl = vi.fn(async () => new Response("{}", { status: 200 }));
    await decideInboxItem(item("party"), "reject", fetchImpl);
    expect(fetchImpl).toHaveBeenCalledWith("/api/v1/party/pending-changes/c1:reject", expect.objectContaining({ method: "POST" }));
  });

  it("refuses sources that have no decision endpoint", async () => {
    const fetchImpl = vi.fn();
    await expect(decideInboxItem(item("ledger"), "approve", fetchImpl)).rejects.toThrow(/ledger/);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
