import { describe, expect, it, vi } from "vitest";
import { ApplicationFailure } from "@temporalio/activity";
import { createPartyMergeHttpClient, STALE_MERGE_REASON, type PartyMergeDecision } from "../src/partyMergeActivities.js";
import { mergeRequestedType, toMergeApprovalInput, type MergeRequestedEvent } from "../src/changeRequested.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import { validateInput, type ApprovalLevel } from "../src/workflows.js";
import { decodeClaims } from "./fakePayables.js";

const approval: PartyMergeDecision = { mergeRequestId: "mr-1", tenantId: "t1", decision: "approved", decidedBy: "alice" };
const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 9).toString("base64") });
const levels: ApprovalLevel[] = [{ id: "data-steward", approver: "alice", escalationApprover: "cfo", slaMs: 3_600_000 }];

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
  return { fetchImpl, log, client: createPartyMergeHttpClient("http://party", signer, fetchImpl, log) };
}

describe("party merge client", () => {
  it("approves under a tenant-level workflow-ts token for the approver: no legal entity, the merge duty only", async () => {
    const { fetchImpl, client } = clientWith(new Response(null, { status: 200 }));
    await client.decideMerge(approval);
    const [url, init] = fetchImpl.mock.calls[0]!;
    expect(url).toBe("http://party/v1/party/merge-requests/mr-1:approve");
    const claims = claimsOf(init);
    expect(claims).toMatchObject({ iss: "workflow-ts", aud: "party", tid: "t1", act: "alice", duties: ["party.merge.execute"] });
    expect(claims).not.toHaveProperty("le");
    const headers = new Headers(init?.headers);
    for (const name of ["X-Passage-Tenant", "X-Passage-Legal-Entity", "X-Passage-User", "X-Passage-Duties"]) expect(headers.has(name)).toBe(false);
    expect(init?.body).toBeUndefined();
  });

  it("rejects with no body for a rejection", async () => {
    const { fetchImpl, client } = clientWith(new Response(null, { status: 200 }));
    await client.decideMerge({ ...approval, decision: "rejected" });
    const [url, init] = fetchImpl.mock.calls[0]!;
    expect(url).toBe("http://party/v1/party/merge-requests/mr-1:reject");
    expect(init?.body).toBeUndefined();
  });

  it("makes the requester deciding their own merge final, and keeps a refused token retryable", async () => {
    const { client } = clientWith(problem(403, "PTY-MERGE-SELF-APPROVAL"));
    const error = await client.decideMerge(approval).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApplicationFailure);
    expect((error as ApplicationFailure).nonRetryable).toBe(true);
    for (const response of [problem(401, "AUTH-0001"), problem(403, "AUTH-0003"), new Response(null, { status: 503 })]) {
      const { client } = clientWith(response);
      const retryable = await client.decideMerge(approval).catch((e: unknown) => e);
      expect(retryable).toBeInstanceOf(Error);
      expect(retryable).not.toBeInstanceOf(ApplicationFailure);
    }
  });

  it("treats an already-decided merge as terminal success, so a replayed activity merges nothing twice", async () => {
    const { fetchImpl, log, client } = clientWith(problem(409, "PTY-MERGE-DECIDED"));
    await expect(client.decideMerge(approval)).resolves.toBeUndefined();
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(log).toHaveBeenCalledWith("party merge already decided", { mergeRequestId: "mr-1", action: "approve" });
  });

  it("rejects a stale merge as the same approver with the stale reason", async () => {
    const { fetchImpl, client } = clientWith(problem(409, "PTY-MERGE-STALE"), new Response(null, { status: 200 }));
    await client.decideMerge(approval);
    const [url, init] = fetchImpl.mock.calls[1]!;
    expect(url).toBe("http://party/v1/party/merge-requests/mr-1:reject");
    expect(claimsOf(init)).toMatchObject({ act: "alice", duties: ["party.merge.execute"] });
    expect(JSON.parse(init?.body as string)).toEqual({ reason: STALE_MERGE_REASON });
  });

  it("refuses, without calling party, to mint without a tenant or approver", async () => {
    const { fetchImpl, client } = clientWith(new Response(null, { status: 200 }));
    const error = await client.decideMerge({ ...approval, decidedBy: "" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApplicationFailure);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});

describe("merge.requested consumer", () => {
  const event: MergeRequestedEvent = {
    type: mergeRequestedType,
    tenantid: "tenant-1",
    data: { merge_request_id: "mr-1", survivor_party_id: "p-1", merged_party_id: "p-2", requested_by: "rita" },
  };

  it("maps the event to a tenant-level approval with the requester and its own id space", () => {
    const input = toMergeApprovalInput(event, levels);
    expect(input).toEqual({
      requestId: "party-merge:mr-1",
      entityType: "party_merge",
      levels,
      requester: "rita",
      partyMerge: { mergeRequestId: "mr-1", tenantId: "tenant-1" },
    });
    expect(() => validateInput(input)).not.toThrow();
  });

  it("refuses another type, a missing requester, and a requester among the approvers", () => {
    expect(() => toMergeApprovalInput({ ...event, type: "dev.passage.party.change.requested.v1" }, levels)).toThrow(TypeError);
    expect(() => toMergeApprovalInput({ ...event, data: { ...event.data, requested_by: "" } }, levels)).toThrow(TypeError);
    expect(() => validateInput(toMergeApprovalInput({ ...event, data: { ...event.data, requested_by: "alice" } }, levels))).toThrow(/requester/);
    expect(() => validateInput({ ...toMergeApprovalInput(event, levels), partyChange: { changeId: "c", tenantId: "t", legalEntityId: "l" } })).toThrow(/merge/);
  });
});
