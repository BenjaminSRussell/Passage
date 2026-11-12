import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";
import { runAgent } from "../src/runtime/engine.js";
import { APPROVER, codingDescriptor, FakeApprovals, FakeTools, makeDeps, makeRequest, postDescriptor, proposal, ScriptedProvider, TENANT } from "./fakes.js";

// #605: every run's audit row names where the model call was served, so a residency review can read it back.
describe.skipIf(ownerUrl === undefined)("agent_run records the model endpoint", () => {
  let pg: Fixture;
  beforeAll(async () => {
    pg = await fixture();
  });
  afterAll(async () => {
    await pg.close();
  });

  it("stores provider, host and region with the final status, and reads them back", async () => {
    const endpoint = { provider: "azure-openai", host: "res.openai.azure.com", region: "westeurope" };
    const provider = Object.assign(new ScriptedProvider([proposal(0.8)]), { endpoint });
    const deps = {
      ...makeDeps({
        provider,
        tools: new FakeTools({ "passage.ap.invoice.post": postDescriptor, "passage.ap.invoice.propose_coding": codingDescriptor }),
        approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }),
      }),
      audit: pg.store,
    };
    const result = await runAgent(deps, makeRequest({ runId: "run-eu-1" }));
    expect(result.modelEndpoint).toEqual(endpoint);
    const stored = await pg.store.getRun(TENANT, "run-eu-1");
    expect(stored?.modelEndpoint).toEqual(endpoint);
  });

  it("a run that made no model call stores none", async () => {
    const deps = {
      ...makeDeps({
        provider: new ScriptedProvider([]),
        tools: new FakeTools({}),
        approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }),
      }),
      audit: pg.store,
      killSwitch: { isHalted: () => true },
    };
    await expect(runAgent(deps, makeRequest({ runId: "run-eu-2" }))).rejects.toThrow("AGT-0201");
    expect((await pg.store.getRun(TENANT, "run-eu-2"))?.modelEndpoint).toBeUndefined();
  });
});
