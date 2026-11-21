import { describe, expect, it, vi } from "vitest";
import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy, WorkflowNotFoundError, type Client } from "@temporalio/client";
import {
  createEventHandler,
  DEFAULT_DLQ_TOPIC,
  DEFAULT_GROUP,
  eventConsumerConfigFromEnv,
  TENDER_TOPICS,
  type DeadLetterRecord,
  type EventMessage,
} from "../src/eventConsumer.js";
import { ecoSubmittedSource, ecoSubmittedType, nonconformanceCreatedSource, nonconformanceCreatedType } from "../src/workflowTriggers.js";
import { loadReleasedSource, loadReleasedType } from "../src/tenderTriggers.js";
import { planSubmittedSource, planSubmittedType } from "../src/budgetPlanTriggers.js";
import { budgetPlanWorkflow, budgetPlanWorkflowId } from "../src/budgetPlanWorkflow.js";
import { capaWorkflow, ecoApprovalWorkflow, tenderResponseSignal, tenderWorkflow } from "../src/workflows.js";
import { CARRIERS, LOAD_ID } from "./fakeTransport.js";

const TENANT = "00000000-0000-0000-0000-000000000001";
const ENTITY = "00000000-0000-0000-0000-0000000000a1";
const RELEASE = "01a1abf2-4d5a-7674-9348-53a0e1e4248e";

// The envelope transport's outbox writes (services/transport/internal/store/release.go) and Debezium relays as is.
const release = {
  specversion: "1.0",
  id: "01a1abf2-4d5a-7674-9348-53a0e1e42490",
  source: loadReleasedSource,
  type: loadReleasedType,
  subject: LOAD_ID,
  time: "2026-06-01T08:00:00Z",
  datacontenttype: "application/json",
  tenantid: TENANT,
  actor: "planner",
  legalentityid: ENTITY,
  data: {
    releaseId: RELEASE,
    loadId: LOAD_ID,
    legalEntityId: ENTITY,
    releasedBy: "planner",
    legalEntityScope: [ENTITY],
    carriers: [{ carrierId: CARRIERS[0]!, rateQuoted: "120.00" }, { carrierId: CARRIERS[1]! }],
    currency: "USD",
    timeoutMin: 45,
  },
};

const outcome = (type: string) => ({
  specversion: "1.0",
  id: "01a1abf2-4d5a-7674-9348-53a0e1e42491",
  source: loadReleasedSource,
  type,
  subject: "tender-1",
  tenantid: TENANT,
  data: { tenderId: "tender-1", loadId: LOAD_ID, carrierId: CARRIERS[0]!, rank: 1, response: "ACCEPTED" },
});

const PLAN = "00000000-0000-0000-0000-0000000000b1";
const planSubmitted = (overrides: Record<string, unknown> = {}) => ({
  specversion: "1.0",
  id: "01a1abf2-4d5a-7674-9348-53a0e1e42492",
  source: planSubmittedSource,
  type: planSubmittedType,
  subject: PLAN,
  tenantid: TENANT,
  data: { plan_id: PLAN, legal_entity_id: ENTITY, name: "FY27", fiscal_year: 2027, submission: 2, submitted_by: "pat" },
  ...overrides,
});

function message(value: unknown, topic: string = TENDER_TOPICS[0]): EventMessage {
  return {
    topic,
    partition: 2,
    offset: "41",
    key: Buffer.from(LOAD_ID),
    value: value === null ? null : Buffer.from(typeof value === "string" ? value : JSON.stringify(value)),
  };
}

const ECO = "00000000-0000-0000-0000-0000000000e1";
const NCR = "00000000-0000-0000-0000-0000000000c1";
const ecoSubmitted = (data: Record<string, unknown> = { ecoId: ECO, legalEntityId: ENTITY }) => ({
  source: ecoSubmittedSource,
  type: ecoSubmittedType,
  tenantid: TENANT,
  actor: "creator",
  data,
});
const ncrCreated = (data: Record<string, unknown> = { ncr_id: NCR, legal_entity_id: ENTITY }, source = nonconformanceCreatedSource) => ({
  source,
  type: nonconformanceCreatedType,
  tenantid: TENANT,
  data,
});

function setup(start = vi.fn().mockResolvedValue({}), signal = vi.fn().mockResolvedValue(undefined)) {
  const getHandle = vi.fn(() => ({ signal }));
  const client = { workflow: { start, getHandle } } as unknown as Pick<Client, "workflow">;
  const dead: DeadLetterRecord[] = [];
  const deadLetter = vi.fn(async (record: DeadLetterRecord) => {
    dead.push(record);
  });
  const warn = vi.fn();
  const handle = createEventHandler({ client, taskQueue: "depot-approvals", dlqTopic: DEFAULT_DLQ_TOPIC, deadLetter, warn, ecoCapaTriggers: true });
  return { handle, start, signal, getHandle, dead, deadLetter, warn };
}

describe("tender event consumer", () => {
  it("is off without brokers and otherwise reads the transport and budget plan topics in its own group", () => {
    expect(eventConsumerConfigFromEnv({})).toBeNull();
    expect(eventConsumerConfigFromEnv({ KAFKA_BROKERS: " , " })).toBeNull();
    expect(eventConsumerConfigFromEnv({ KAFKA_BROKERS: "k1:9093, k2:9093" })).toEqual({
      brokers: ["k1:9093", "k2:9093"],
      group: DEFAULT_GROUP,
      topics: ["depot.tms.load", "depot.tms.tender", "depot.budget.plan", "depot.engineering.eco", "depot.quality.nonconformance"],
      dlqTopic: DEFAULT_DLQ_TOPIC,
      ecoCapaTriggers: false,
    });
    expect(
      eventConsumerConfigFromEnv({
        KAFKA_BROKERS: "k1:9093",
        KAFKA_GROUP: "g",
        WORKFLOW_DLQ_TOPIC: "d",
        KAFKA_USERNAME: "workflow-ts-consumer",
        KAFKA_PASSWORD_FILE: "/var/run/kafka/password",
        KAFKA_TLS_CA_FILE: "/var/run/kafka/ca.crt",
      }),
    ).toMatchObject({ group: "g", dlqTopic: "d", username: "workflow-ts-consumer", passwordFile: "/var/run/kafka/password", caFile: "/var/run/kafka/ca.crt" });
  });

  it("starts tenderWorkflow from a load release, with the releasing user, its scope and the release id", async () => {
    const { handle, start, dead } = setup();
    expect(await handle(message(release))).toEqual({ action: "started", workflowId: `tender:${TENANT}:${LOAD_ID}` });
    expect(start).toHaveBeenCalledWith(
      tenderWorkflow,
      expect.objectContaining({
        workflowId: `tender:${TENANT}:${LOAD_ID}`,
        taskQueue: "depot-approvals",
        workflowIdReusePolicy: WorkflowIdReusePolicy.ALLOW_DUPLICATE,
        args: [
          {
            tenantId: TENANT,
            legalEntityId: ENTITY,
            loadId: LOAD_ID,
            legalEntityScope: [ENTITY],
            carriers: [{ carrierId: CARRIERS[0], rateQuoted: "120.00" }, { carrierId: CARRIERS[1] }],
            currency: "USD",
            timeoutMin: 45,
            requestedBy: "planner",
            releaseId: RELEASE,
          },
        ],
      }),
    );
    expect(dead).toEqual([]);
  });

  it("commits a redelivered release while its tender is still running", async () => {
    const { handle } = setup(vi.fn().mockRejectedValue(new WorkflowExecutionAlreadyStartedError("running", "tender", "tenderWorkflow")));
    expect(await handle(message(release))).toEqual({ action: "already-running", workflowId: `tender:${TENANT}:${LOAD_ID}` });
  });

  it("sends the tender-response signal on a carrier's acceptance or decline, and ignores an expiry", async () => {
    const { handle, signal, getHandle } = setup();
    for (const type of ["dev.depot.tms.tender.accepted.v1", "dev.depot.tms.tender.declined.v1"]) {
      expect(await handle(message(outcome(type), TENDER_TOPICS[1]))).toEqual({ action: "signalled", workflowId: `tender:${TENANT}:${LOAD_ID}` });
    }
    expect(getHandle).toHaveBeenCalledWith(`tender:${TENANT}:${LOAD_ID}`);
    expect(signal).toHaveBeenCalledTimes(2);
    expect(signal).toHaveBeenCalledWith(tenderResponseSignal);
    expect(await handle(message(outcome("dev.depot.tms.tender.expired.v1"), TENDER_TOPICS[1]))).toEqual({
      action: "ignored",
      type: "dev.depot.tms.tender.expired.v1",
    });
    expect(signal).toHaveBeenCalledTimes(2);
  });

  it("commits an outcome for a tender no workflow follows (a planner's own tender)", async () => {
    const { handle, dead } = setup(vi.fn(), vi.fn().mockRejectedValue(new WorkflowNotFoundError("none", "tender", undefined)));
    expect(await handle(message(outcome("dev.depot.tms.tender.accepted.v1"), TENDER_TOPICS[1]))).toEqual({
      action: "no-workflow",
      workflowId: `tender:${TENANT}:${LOAD_ID}`,
    });
    expect(dead).toEqual([]);
  });

  it("ignores the other events of the topics", async () => {
    const { handle, start, getHandle } = setup();
    expect(await handle(message({ ...release, type: "dev.depot.tms.load.built.v1" }))).toEqual({ action: "ignored", type: "dev.depot.tms.load.built.v1" });
    expect(start).not.toHaveBeenCalled();
    expect(getHandle).not.toHaveBeenCalled();
  });

  it("dead-letters what it can never act on, with the reason and where it came from, and starts nothing", async () => {
    const cases: [unknown, RegExp][] = [
      ["{not json", /not JSON/],
      [null, /not a JSON object/],
      [[release], /not a JSON object/],
      [{ ...release, type: undefined }, /missing type/],
      [{ ...release, data: undefined }, /missing data/],
      [{ ...release, source: "/depot/tms/edi" }, /accepted only from \/depot\/tms\/transport/],
      [{ ...release, data: { ...release.data, carriers: [] } }, /carrier/],
      [{ ...release, data: { ...release.data, legalEntityScope: ["00000000-0000-0000-0000-0000000000a2"] } }, /legalEntityScope/],
      [{ ...release, data: { ...release.data, releasedBy: "" } }, /required/],
      [{ ...outcome("dev.depot.tms.tender.accepted.v1"), tenantid: "" }, /tenant/],
    ];
    for (const [value, reason] of cases) {
      const { handle, start, getHandle, dead } = setup();
      const result = await handle(message(value));
      expect(result).toMatchObject({ action: "dead-lettered", reason: expect.stringMatching(reason) });
      expect(dead).toHaveLength(1);
      expect(dead[0]).toMatchObject({
        topic: DEFAULT_DLQ_TOPIC,
        key: Buffer.from(LOAD_ID),
        headers: { "x-dlq-reason": expect.stringMatching(reason), "x-origin-topic": "depot.tms.load", "x-origin-partition": "2", "x-origin-offset": "41" },
      });
      expect(start).not.toHaveBeenCalled();
      expect(getHandle).not.toHaveBeenCalled();
    }
  });

  it("throws, so the message is retried and not committed, when Temporal or the dead-letter topic is unreachable", async () => {
    const down = setup(vi.fn().mockRejectedValue(new Error("14 UNAVAILABLE: connection refused")));
    await expect(down.handle(message(release))).rejects.toThrow(/UNAVAILABLE/);
    expect(down.dead).toEqual([]);
    const signalDown = setup(vi.fn(), vi.fn().mockRejectedValue(new Error("deadline exceeded")));
    await expect(signalDown.handle(message(outcome("dev.depot.tms.tender.declined.v1"), TENDER_TOPICS[1]))).rejects.toThrow(/deadline/);
    const { handle, deadLetter } = setup();
    deadLetter.mockRejectedValueOnce(new Error("broker down"));
    await expect(handle(message("{not json"))).rejects.toThrow(/broker down/);
  });

  it("starts budgetPlanWorkflow from plan.submitted on the budget plan topic, one run per submission", async () => {
    const { handle, start, dead } = setup();
    expect(await handle(message(planSubmitted(), "depot.budget.plan"))).toEqual({ action: "started", workflowId: budgetPlanWorkflowId(PLAN, 2) });
    expect(start).toHaveBeenCalledWith(
      budgetPlanWorkflow,
      expect.objectContaining({
        workflowId: `budget-plan:${PLAN}:2`,
        taskQueue: "depot-approvals",
        workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
        args: [{ tenantId: TENANT, legalEntityId: ENTITY, planId: PLAN, submission: 2, requester: "pat" }],
      }),
    );
    expect(dead).toEqual([]);
  });

  it("commits a redelivered plan.submitted while its workflow exists and ignores the plan's other events", async () => {
    const { handle, start } = setup(vi.fn().mockRejectedValue(new WorkflowExecutionAlreadyStartedError("running", "budget-plan", "budgetPlanWorkflow")));
    expect(await handle(message(planSubmitted(), "depot.budget.plan"))).toEqual({ action: "already-running", workflowId: budgetPlanWorkflowId(PLAN, 2) });
    for (const type of ["dev.depot.budget.plan.approved.v1", "dev.depot.budget.plan.rejected.v1"]) {
      expect(await handle(message(planSubmitted({ type }), "depot.budget.plan"))).toEqual({ action: "ignored", type });
    }
    expect(start).toHaveBeenCalledTimes(1);
  });

  it("dead-letters a plan.submitted from another source or without a valid submission, and starts nothing", async () => {
    const { handle, start, dead } = setup();
    const forged = await handle(message(planSubmitted({ source: "/depot/tms/transport" }), "depot.budget.plan"));
    expect(forged).toMatchObject({ action: "dead-lettered" });
    const bad = await handle(
      message(planSubmitted({ data: { plan_id: PLAN, legal_entity_id: ENTITY, submission: 0, submitted_by: "pat" } }), "depot.budget.plan"),
    );
    expect(bad).toMatchObject({ action: "dead-lettered", reason: "submission must be a positive integer" });
    expect(start).not.toHaveBeenCalled();
    expect(dead.map((d) => d.headers["x-origin-topic"])).toEqual(["depot.budget.plan", "depot.budget.plan"]);
  });

  it("throws, so the message is retried, when Temporal is unreachable for a plan.submitted", async () => {
    const { handle, dead } = setup(vi.fn().mockRejectedValue(new Error("temporal unavailable")));
    await expect(handle(message(planSubmitted(), "depot.budget.plan"))).rejects.toThrow("temporal unavailable");
    expect(dead).toEqual([]);
  });

  it("turns the ECO and CAPA triggers on only when WORKFLOW_ECO_CAPA_TRIGGERS is exactly true", () => {
    const on = (flag?: string) => eventConsumerConfigFromEnv({ KAFKA_BROKERS: "k1:9093", ...(flag === undefined ? {} : { WORKFLOW_ECO_CAPA_TRIGGERS: flag }) })?.ecoCapaTriggers;
    for (const flag of [undefined, "false", "TRUE", ""]) expect(on(flag)).toBe(false);
    expect(on("true")).toBe(true);
  });

  it("ignores eco.submitted and nonconformance.created while the triggers are off", async () => {
    const start = vi.fn();
    const off = createEventHandler({
      client: { workflow: { start } } as unknown as Parameters<typeof createEventHandler>[0]["client"],
      taskQueue: "depot-approvals",
      dlqTopic: DEFAULT_DLQ_TOPIC,
      deadLetter: async () => undefined,
    });
    expect(await off(message(ecoSubmitted(), "depot.engineering.eco"))).toMatchObject({ action: "ignored" });
    expect(await off(message(ncrCreated(), "depot.quality.nonconformance"))).toMatchObject({ action: "ignored" });
    expect(start).not.toHaveBeenCalled();
  });

  it("starts ecoApprovalWorkflow from eco.submitted and capaWorkflow from nonconformance.created", async () => {
    const { handle, start, dead } = setup();
    expect(await handle(message(ecoSubmitted(), "depot.engineering.eco"))).toEqual({ action: "started", workflowId: `eco-approval:${ECO}` });
    expect(await handle(message(ncrCreated(), "depot.quality.nonconformance"))).toEqual({ action: "started", workflowId: `capa:${NCR}` });
    expect(start).toHaveBeenNthCalledWith(1, ecoApprovalWorkflow, expect.objectContaining({ workflowId: `eco-approval:${ECO}` }));
    expect(start).toHaveBeenNthCalledWith(2, capaWorkflow, expect.objectContaining({ workflowId: `capa:${NCR}` }));
    expect(dead).toEqual([]);
  });

  it("commits an event without a legal entity as skipped, with a warning and no start or dead letter", async () => {
    const { handle, start, dead, warn } = setup();
    for (const [event, workflowId] of [
      [ecoSubmitted({ ecoId: ECO }), `eco-approval:${ECO}`],
      [ncrCreated({ ncr_id: NCR }), `capa:${NCR}`],
    ] as const) {
      expect(await handle(message(event, "depot.engineering.eco"))).toMatchObject({ action: "skipped", workflowId, reason: expect.stringContaining("no legal entity") });
    }
    expect(start).not.toHaveBeenCalled();
    expect(dead).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(2);
    expect(warn).toHaveBeenCalledWith("workflow not started", expect.objectContaining({ workflowId: `capa:${NCR}`, reason: expect.any(String) }));
  });

  it("dead-letters an ncr event from a foreign source and commits a redelivered eco.submitted", async () => {
    const { handle, start, dead } = setup();
    expect(await handle(message(ncrCreated(undefined, "/depot/engineering/engineering"), "depot.quality.nonconformance"))).toMatchObject({ action: "dead-lettered" });
    expect(dead).toHaveLength(1);
    expect(start).not.toHaveBeenCalled();
    const again = setup(vi.fn().mockRejectedValue(new WorkflowExecutionAlreadyStartedError("running", "eco", "ecoApprovalWorkflow")));
    expect(await again.handle(message(ecoSubmitted(), "depot.engineering.eco"))).toEqual({ action: "already-running", workflowId: `eco-approval:${ECO}` });
  });
});
