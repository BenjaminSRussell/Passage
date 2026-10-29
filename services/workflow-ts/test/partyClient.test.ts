import { describe, expect, it, vi } from "vitest";
import { ApplicationFailure } from "@temporalio/activity";
import { createPartyHttpClient, STALE_REJECT_REASON, type PartyDecision } from "../src/activities.js";

const approval: PartyDecision = {
  changeId: "chg-1",
  tenantId: "t1",
  legalEntityId: "le1",
  decision: "approved",
  decidedBy: "alice",
};

function problem(status: number, code: string): Response {
  return new Response(JSON.stringify({ status, code }), { status, headers: { "Content-Type": "application/problem+json" } });
}

function clientWith(...responses: Response[]) {
  const fetchImpl = vi.fn<typeof fetch>();
  for (const response of responses) fetchImpl.mockResolvedValueOnce(response);
  const log = vi.fn();
  return { fetchImpl, log, client: createPartyHttpClient("http://party", fetchImpl, log) };
}

describe("party http client", () => {
  it("posts the approval with identity and duty headers", async () => {
    const { fetchImpl, client } = clientWith(new Response(null, { status: 200 }));
    await client.decide(approval);
    const [url, init] = fetchImpl.mock.calls[0]!;
    expect(url).toBe("http://party/v1/party/pending-changes/chg-1:approve");
    expect(init?.headers).toMatchObject({
      "X-Passage-Tenant": "t1",
      "X-Passage-Legal-Entity": "le1",
      "X-Passage-User": "alice",
      "X-Passage-Duties": "party.approve",
    });
  });

  it("rejects with the stale reason as the same decider when approval is stale", async () => {
    const { fetchImpl, client } = clientWith(problem(409, "PTY-PENDING-STALE"), new Response(null, { status: 200 }));
    await client.decide(approval);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    const [url, init] = fetchImpl.mock.calls[1]!;
    expect(url).toBe("http://party/v1/party/pending-changes/chg-1:reject");
    expect(init?.headers).toMatchObject({ "X-Passage-User": "alice", "X-Passage-Duties": "party.approve" });
    expect(JSON.parse(init?.body as string)).toEqual({ reason: STALE_REJECT_REASON });
  });

  it("treats an already-decided response as terminal success", async () => {
    const { fetchImpl, log, client } = clientWith(problem(409, "PTY-CHANGE-DECIDED"));
    await expect(client.decide(approval)).resolves.toBeUndefined();
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(log).toHaveBeenCalledWith("party change already decided", { changeId: "chg-1", action: "approve" });
  });

  it("treats an already-decided response to the stale reject as terminal success", async () => {
    const { client } = clientWith(problem(409, "PTY-PENDING-STALE"), problem(409, "PTY-CHANGE-DECIDED"));
    await expect(client.decide(approval)).resolves.toBeUndefined();
  });

  it("fails non-retryably on other 4xx", async () => {
    const { client } = clientWith(problem(403, "PTY-FORBIDDEN"));
    const error = await client.decide(approval).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApplicationFailure);
    expect((error as ApplicationFailure).nonRetryable).toBe(true);
  });

  it("keeps 429 retryable", async () => {
    const { client } = clientWith(new Response(null, { status: 429 }));
    const error = await client.decide(approval).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(Error);
    expect(error).not.toBeInstanceOf(ApplicationFailure);
  });

  it("keeps 5xx retryable", async () => {
    const { client } = clientWith(new Response(null, { status: 503 }));
    const error = await client.decide(approval).catch((e: unknown) => e);
    expect(error).not.toBeInstanceOf(ApplicationFailure);
  });
});
