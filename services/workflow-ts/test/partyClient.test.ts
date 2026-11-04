import { describe, expect, it, vi } from "vitest";
import { ApplicationFailure } from "@temporalio/activity";
import { createPartyHttpClient, STALE_REJECT_REASON, type PartyDecision } from "../src/activities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import { decodeClaims } from "./fakePayables.js";

const approval: PartyDecision = {
  changeId: "chg-1",
  tenantId: "t1",
  legalEntityId: "le1",
  decision: "approved",
  decidedBy: "alice",
  scope: ["le0", "le1"],
};
const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 9).toString("base64") });

function claimsOf(init: RequestInit | undefined) {
  return decodeClaims(new Headers(init?.headers).get("X-Passage-Service-Token"));
}

function problem(status: number, code: string): Response {
  return new Response(JSON.stringify({ status, code }), { status, headers: { "Content-Type": "application/problem+json" } });
}

function clientWith(...responses: Response[]) {
  const fetchImpl = vi.fn<typeof fetch>();
  for (const response of responses) fetchImpl.mockResolvedValueOnce(response);
  const log = vi.fn();
  return { fetchImpl, log, client: createPartyHttpClient("http://party", signer, fetchImpl, log) };
}

describe("party http client", () => {
  it("posts the approval under a workflow-ts service token acting for the approver, and no user headers", async () => {
    const { fetchImpl, client } = clientWith(new Response(null, { status: 200 }));
    await client.decide(approval);
    const [url, init] = fetchImpl.mock.calls[0]!;
    expect(url).toBe("http://party/v1/party/pending-changes/chg-1:approve");
    expect(claimsOf(init)).toMatchObject({ iss: "workflow-ts", aud: "party", tid: "t1", le: "le1", act: "alice", duties: ["party.approve"] });
    const headers = new Headers(init?.headers);
    for (const name of ["X-Passage-Tenant", "X-Passage-Legal-Entity", "X-Passage-User", "X-Passage-Duties"]) expect(headers.has(name)).toBe(false);
  });

  it("refuses, without calling party, a decision whose approver's scope lacks the change's legal entity", async () => {
    const { fetchImpl, client } = clientWith(new Response(null, { status: 200 }));
    const error = await client.decide({ ...approval, scope: ["le0"] }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApplicationFailure);
    expect((error as ApplicationFailure).nonRetryable).toBe(true);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("keeps a refused service token retryable", async () => {
    for (const response of [problem(401, "AUTH-0001"), problem(403, "AUTH-0003")]) {
      const { client } = clientWith(response);
      const error = await client.decide(approval).catch((e: unknown) => e);
      expect(error).toBeInstanceOf(Error);
      expect(error).not.toBeInstanceOf(ApplicationFailure);
    }
  });

  it("rejects with the stale reason as the same decider when approval is stale", async () => {
    const { fetchImpl, client } = clientWith(problem(409, "PTY-PENDING-STALE"), new Response(null, { status: 200 }));
    await client.decide(approval);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    const [url, init] = fetchImpl.mock.calls[1]!;
    expect(url).toBe("http://party/v1/party/pending-changes/chg-1:reject");
    expect(claimsOf(init)).toMatchObject({ act: "alice", le: "le1", duties: ["party.approve"] });
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
    const { client } = clientWith(problem(403, "PTY-PENDING-SELF-APPROVAL"));
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
