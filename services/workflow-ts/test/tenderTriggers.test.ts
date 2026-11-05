import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";
import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy, WorkflowNotFoundError, type Client } from "@temporalio/client";
import { handleLoadReleased, handleTenderOutcome, loadReleasedSource, loadReleasedType, toTenderInput, type LoadReleasedEvent } from "../src/tenderTriggers.js";
import { tenderResponseSignal, tenderWorkflow } from "../src/workflows.js";
import { CARRIERS, LOAD_ID } from "./fakeTransport.js";

const released: LoadReleasedEvent = {
  type: loadReleasedType,
  source: loadReleasedSource,
  tenantid: "00000000-0000-0000-0000-000000000001",
  data: {
    loadId: LOAD_ID,
    legalEntityId: "00000000-0000-0000-0000-0000000000a1",
    releasedBy: "planner",
    legalEntityScope: ["00000000-0000-0000-0000-0000000000a1"],
    carriers: [{ carrierId: CARRIERS[0]!, rateQuoted: "120.00" }, { carrierId: CARRIERS[1]! }],
    currency: "USD",
    timeoutMin: 45,
  },
};

function clientWith(start: ReturnType<typeof vi.fn>, signal = vi.fn()) {
  const getHandle = vi.fn(() => ({ signal }));
  return { client: { workflow: { start, getHandle } } as unknown as Pick<Client, "workflow">, start, signal, getHandle };
}

describe("tender triggers", () => {
  it("maps a load release to the tender input with the releasing user as the requester", () => {
    expect(toTenderInput(released)).toEqual({
      tenantId: released.tenantid,
      legalEntityId: released.data.legalEntityId,
      loadId: LOAD_ID,
      legalEntityScope: released.data.legalEntityScope,
      carriers: [{ carrierId: CARRIERS[0], rateQuoted: "120.00" }, { carrierId: CARRIERS[1] }],
      currency: "USD",
      timeoutMin: 45,
      requestedBy: "planner",
    });
  });

  it("carries the release id into the tender input and refuses one that is not a UUID", () => {
    const releaseId = "01a1abf2-4d5a-7674-9348-53a0e1e4248e";
    expect(toTenderInput({ ...released, data: { ...released.data, releaseId } })).toMatchObject({ releaseId });
    expect(() => toTenderInput({ ...released, data: { ...released.data, releaseId: "r-1" } })).toThrow(/releaseId/);
  });

  it("starts tender:<loadId>, reports a run already in flight and allows a re-release after the last run closed", async () => {
    const { client, start } = clientWith(vi.fn().mockResolvedValueOnce({}).mockRejectedValueOnce(new WorkflowExecutionAlreadyStartedError("dup", "tender", "tenderWorkflow")));
    expect(await handleLoadReleased(client, "q", released)).toEqual({ workflowId: `tender:${released.tenantid}:${LOAD_ID}`, started: true });
    expect(start).toHaveBeenCalledWith(tenderWorkflow, expect.objectContaining({ workflowId: `tender:${released.tenantid}:${LOAD_ID}`, taskQueue: "q", workflowIdReusePolicy: WorkflowIdReusePolicy.ALLOW_DUPLICATE }));
    expect(await handleLoadReleased(client, "q", released)).toEqual({ workflowId: `tender:${released.tenantid}:${LOAD_ID}`, started: false });
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
      expect(await handleTenderOutcome(client, { type, tenantid: released.tenantid, data: { loadId: LOAD_ID } })).toEqual({ workflowId: `tender:${released.tenantid}:${LOAD_ID}`, signalled: true });
    }
    expect(signal).toHaveBeenCalledTimes(2);
    expect(signal).toHaveBeenCalledWith(tenderResponseSignal);
  });

  it("signals only the workflow of the event's own tenant", async () => {
    const { client, getHandle } = clientWith(vi.fn());
    const otherTenant = "00000000-0000-0000-0000-000000000002";
    await handleTenderOutcome(client, { type: "dev.passage.tms.tender.accepted.v1", tenantid: otherTenant, data: { loadId: LOAD_ID } });
    expect(getHandle).toHaveBeenCalledWith(`tender:${otherTenant}:${LOAD_ID}`);
    expect(getHandle).not.toHaveBeenCalledWith(`tender:${released.tenantid}:${LOAD_ID}`);
    await expect(handleTenderOutcome(client, { type: "dev.passage.tms.tender.accepted.v1", tenantid: "", data: { loadId: LOAD_ID } })).rejects.toThrow(/tenant/);
  });

  it("refuses a release whose scope does not cover its legal entity", async () => {
    const { client, start } = clientWith(vi.fn());
    await expect(handleLoadReleased(client, "q", { ...released, data: { ...released.data, legalEntityScope: [] } })).rejects.toThrow(/legalEntityScope/);
    expect(start).not.toHaveBeenCalled();
  });

  it("refuses a release from any source but transport, whose verified principal fills the scope", async () => {
    const { client, start } = clientWith(vi.fn());
    for (const source of ["/passage/tms/edi", "", "/passage/tms/transport/"]) {
      await expect(handleLoadReleased(client, "q", { ...released, source })).rejects.toThrow(/accepted only from \/passage\/tms\/transport/);
    }
    expect(start).not.toHaveBeenCalled();
  });

  it("states in the load.released contract that transport fills releasedBy and legalEntityScope from the verified principal", async () => {
    const read = (path: string) => readFile(fileURLToPath(new URL(path, import.meta.url)), "utf8");
    const schema = JSON.parse(await read("../schemas/current/tms.load.released.v1.json"));
    expect(schema.properties.source.enum).toEqual([loadReleasedSource]);
    expect(schema.properties.type.enum).toEqual([loadReleasedType]);
    expect(schema.properties.data.required).toEqual(expect.arrayContaining(["releasedBy", "legalEntityScope", "legalEntityId"]));
    expect(schema.description).toMatch(/MUST fill data\.releasedBy and data\.legalEntityScope from the gateway-verified principal/);
    expect(schema.properties.data.properties.releasedBy.description).toMatch(/verified principal/);
    expect(schema.properties.data.properties.legalEntityScope.description).toMatch(/verified principal/);
    const asyncapi = await read("../events/asyncapi.yaml");
    expect(asyncapi).toContain("name: dev.passage.tms.load.released.v1");
    expect(asyncapi).toContain("$ref: ../schemas/current/tms.load.released.v1.json");
    expect(asyncapi).toMatch(/gateway-verified principal/);
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
