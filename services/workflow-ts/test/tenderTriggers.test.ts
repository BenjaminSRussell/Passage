import { describe, expect, it, vi } from "vitest";
import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy, WorkflowNotFoundError, type Client } from "@temporalio/client";
import { handleLoadReleased, handleTenderOutcome, loadReleasedType, toTenderInput, type LoadReleasedEvent } from "../src/tenderTriggers.js";
import { tenderResponseSignal, tenderWorkflow } from "../src/workflows.js";
import { CARRIERS, LOAD_ID } from "./fakeTransport.js";

const released: LoadReleasedEvent = {
  type: loadReleasedType,
  tenantid: "00000000-0000-0000-0000-000000000001",
  data: {
    loadId: LOAD_ID,
    legalEntityId: "00000000-0000-0000-0000-0000000000a1",
    releasedBy: "planner",
    carriers: [{ carrierId: CARRIERS[0]!, rateQuoted: "120.00" }, { carrierId: CARRIERS[1]! }],
    currency: "USD",
    timeoutMin: 45,
  },
};

function clientWith(start: ReturnType<typeof vi.fn>, signal = vi.fn()) {
  return { client: { workflow: { start, getHandle: vi.fn(() => ({ signal })) } } as unknown as Pick<Client, "workflow">, start, signal };
}

describe("tender triggers", () => {
  it("maps a load release to the tender input with the releasing user as the requester", () => {
    expect(toTenderInput(released)).toEqual({
      tenantId: released.tenantid,
      legalEntityId: released.data.legalEntityId,
      loadId: LOAD_ID,
      carriers: [{ carrierId: CARRIERS[0], rateQuoted: "120.00" }, { carrierId: CARRIERS[1] }],
      currency: "USD",
      timeoutMin: 45,
      requestedBy: "planner",
    });
  });

  it("starts tender:<loadId>, reports a run already in flight and allows a re-release after the last run closed", async () => {
    const { client, start } = clientWith(vi.fn().mockResolvedValueOnce({}).mockRejectedValueOnce(new WorkflowExecutionAlreadyStartedError("dup", "tender", "tenderWorkflow")));
    expect(await handleLoadReleased(client, "q", released)).toEqual({ workflowId: `tender:${LOAD_ID}`, started: true });
    expect(start).toHaveBeenCalledWith(tenderWorkflow, expect.objectContaining({ workflowId: `tender:${LOAD_ID}`, taskQueue: "q", workflowIdReusePolicy: WorkflowIdReusePolicy.ALLOW_DUPLICATE }));
    expect(await handleLoadReleased(client, "q", released)).toEqual({ workflowId: `tender:${LOAD_ID}`, started: false });
  });

  it("starts nothing for another event type, a missing carrier list or a missing releasing user", async () => {
    const { client, start } = clientWith(vi.fn());
    await expect(handleLoadReleased(client, "q", { ...released, type: "dev.passage.tms.load.built.v1" })).rejects.toThrow(/expected/);
    await expect(handleLoadReleased(client, "q", { ...released, data: { ...released.data, carriers: [] } })).rejects.toThrow(/carrier/);
    await expect(handleLoadReleased(client, "q", { ...released, data: { ...released.data, releasedBy: "" } })).rejects.toThrow(/required/);
    expect(start).not.toHaveBeenCalled();
  });

  it("signals the running tender workflow when a carrier accepts or declines", async () => {
    const { client, signal } = clientWith(vi.fn());
    for (const type of ["dev.passage.tms.tender.accepted.v1", "dev.passage.tms.tender.declined.v1"]) {
      expect(await handleTenderOutcome(client, { type, tenantid: released.tenantid, data: { loadId: LOAD_ID } })).toEqual({ workflowId: `tender:${LOAD_ID}`, signalled: true });
    }
    expect(signal).toHaveBeenCalledTimes(2);
    expect(signal).toHaveBeenCalledWith(tenderResponseSignal);
  });

  it("ignores an outcome for a tender no workflow is running", async () => {
    const { client } = clientWith(vi.fn(), vi.fn().mockRejectedValue(new WorkflowNotFoundError("none", "tender", "")));
    expect(await handleTenderOutcome(client, { type: "dev.passage.tms.tender.accepted.v1", tenantid: released.tenantid, data: { loadId: LOAD_ID } })).toMatchObject({ signalled: false });
  });

  it("does not signal for the expiry event, which the workflow's own sweep causes", async () => {
    const { client, signal } = clientWith(vi.fn());
    await expect(handleTenderOutcome(client, { type: "dev.passage.tms.tender.expired.v1", tenantid: released.tenantid, data: { loadId: LOAD_ID } })).rejects.toThrow(/expected/);
    expect(signal).not.toHaveBeenCalled();
  });
});
