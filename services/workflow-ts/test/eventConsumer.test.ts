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
import { loadReleasedSource, loadReleasedType } from "../src/tenderTriggers.js";
import { planSubmittedSource, planSubmittedType } from "../src/budgetPlanTriggers.js";
import { budgetPlanWorkflow, budgetPlanWorkflowId } from "../src/budgetPlanWorkflow.js";
import { tenderResponseSignal, tenderWorkflow } from "../src/workflows.js";
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

function setup(start = vi.fn().mockResolvedValue({}), signal = vi.fn().mockResolvedValue(undefined)) {
  const getHandle = vi.fn(() => ({ signal }));
  const client = { workflow: { start, getHandle } } as unknown as Pick<Client, "workflow">;
  const dead: DeadLetterRecord[] = [];
  const deadLetter = vi.fn(async (record: DeadLetterRecord) => {
    dead.push(record);
  });
  const handle = createEventHandler({ client, taskQueue: "passage-approvals", dlqTopic: DEFAULT_DLQ_TOPIC, deadLetter });
  return { handle, start, signal, getHandle, dead, deadLetter };
}

describe("tender event consumer", () => {
  it("is off without brokers and otherwise reads the transport and budget plan topics in its own group", () => {
    expect(eventConsumerConfigFromEnv({})).toBeNull();
    expect(eventConsumerConfigFromEnv({ KAFKA_BROKERS: " , " })).toBeNull();
    expect(eventConsumerConfigFromEnv({ KAFKA_BROKERS: "k1:9093, k2:9093" })).toEqual({
      brokers: ["k1:9093", "k2:9093"],
      group: DEFAULT_GROUP,
      topics: ["passage.tms.load", "passage.tms.tender", "passage.budget.plan"],
      dlqTopic: DEFAULT_DLQ_TOPIC,
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
        taskQueue: "passage-approvals",
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
    for (const type of ["dev.passage.tms.tender.accepted.v1", "dev.passage.tms.tender.declined.v1"]) {
      expect(await handle(message(outcome(type), TENDER_TOPICS[1]))).toEqual({ action: "signalled", workflowId: `tender:${TENANT}:${LOAD_ID}` });
    }
    expect(getHandle).toHaveBeenCalledWith(`tender:${TENANT}:${LOAD_ID}`);
    expect(signal).toHaveBeenCalledTimes(2);
    expect(signal).toHaveBeenCalledWith(tenderResponseSignal);
    expect(await handle(message(outcome("dev.passage.tms.tender.expired.v1"), TENDER_TOPICS[1]))).toEqual({
      action: "ignored",
      type: "dev.passage.tms.tender.expired.v1",
    });
    expect(signal).toHaveBeenCalledTimes(2);
  });

  it("commits an outcome for a tender no workflow follows (a planner's own tender)", async () => {
    const { handle, dead } = setup(vi.fn(), vi.fn().mockRejectedValue(new WorkflowNotFoundError("none", "tender", undefined)));
    expect(await handle(message(outcome("dev.passage.tms.tender.accepted.v1"), TENDER_TOPICS[1]))).toEqual({
      action: "no-workflow",
      workflowId: `tender:${TENANT}:${LOAD_ID}`,
    });
    expect(dead).toEqual([]);
  });

  it("ignores the other events of the topics", async () => {
    const { handle, start, getHandle } = setup();
    expect(await handle(message({ ...release, type: "dev.passage.tms.load.built.v1" }))).toEqual({ action: "ignored", type: "dev.passage.tms.load.built.v1" });
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
      [{ ...release, source: "/passage/tms/edi" }, /accepted only from \/passage\/tms\/transport/],
      [{ ...release, data: { ...release.data, carriers: [] } }, /carrier/],
      [{ ...release, data: { ...release.data, legalEntityScope: ["00000000-0000-0000-0000-0000000000a2"] } }, /legalEntityScope/],
      [{ ...release, data: { ...release.data, releasedBy: "" } }, /required/],
      [{ ...outcome("dev.passage.tms.tender.accepted.v1"), tenantid: "" }, /tenant/],
    ];
    for (const [value, reason] of cases) {
      const { handle, start, getHandle, dead } = setup();
      const result = await handle(message(value));
      expect(result).toMatchObject({ action: "dead-lettered", reason: expect.stringMatching(reason) });
      expect(dead).toHaveLength(1);
      expect(dead[0]).toMatchObject({
        topic: DEFAULT_DLQ_TOPIC,
        key: Buffer.from(LOAD_ID),
        headers: { "x-dlq-reason": expect.stringMatching(reason), "x-origin-topic": "passage.tms.load", "x-origin-partition": "2", "x-origin-offset": "41" },
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
    await expect(signalDown.handle(message(outcome("dev.passage.tms.tender.declined.v1"), TENDER_TOPICS[1]))).rejects.toThrow(/deadline/);
    const { handle, deadLetter } = setup();
    deadLetter.mockRejectedValueOnce(new Error("broker down"));
    await expect(handle(message("{not json"))).rejects.toThrow(/broker down/);
  });

  it("starts budgetPlanWorkflow from plan.submitted on the budget plan topic, one run per submission", async () => {
    const { handle, start, dead } = setup();
    expect(await handle(message(planSubmitted(), "passage.budget.plan"))).toEqual({ action: "started", workflowId: budgetPlanWorkflowId(PLAN, 2) });
    expect(start).toHaveBeenCalledWith(
      budgetPlanWorkflow,
      expect.objectContaining({
        workflowId: `budget-plan:${PLAN}:2`,
        taskQueue: "passage-approvals",
        workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
        args: [{ tenantId: TENANT, legalEntityId: ENTITY, planId: PLAN, submission: 2, requester: "pat" }],
      }),
    );
    expect(dead).toEqual([]);
  });

  it("commits a redelivered plan.submitted while its workflow exists and ignores the plan's other events", async () => {
    const { handle, start } = setup(vi.fn().mockRejectedValue(new WorkflowExecutionAlreadyStartedError("running", "budget-plan", "budgetPlanWorkflow")));
    expect(await handle(message(planSubmitted(), "passage.budget.plan"))).toEqual({ action: "already-running", workflowId: budgetPlanWorkflowId(PLAN, 2) });
    for (const type of ["dev.passage.budget.plan.approved.v1", "dev.passage.budget.plan.rejected.v1"]) {
      expect(await handle(message(planSubmitted({ type }), "passage.budget.plan"))).toEqual({ action: "ignored", type });
    }
    expect(start).toHaveBeenCalledTimes(1);
  });

  it("dead-letters a plan.submitted from another source or without a valid submission, and starts nothing", async () => {
    const { handle, start, dead } = setup();
    const forged = await handle(message(planSubmitted({ source: "/passage/tms/transport" }), "passage.budget.plan"));
    expect(forged).toMatchObject({ action: "dead-lettered" });
    const bad = await handle(
      message(planSubmitted({ data: { plan_id: PLAN, legal_entity_id: ENTITY, submission: 0, submitted_by: "pat" } }), "passage.budget.plan"),
    );
    expect(bad).toMatchObject({ action: "dead-lettered", reason: "submission must be a positive integer" });
    expect(start).not.toHaveBeenCalled();
    expect(dead.map((d) => d.headers["x-origin-topic"])).toEqual(["passage.budget.plan", "passage.budget.plan"]);
  });

  it("throws, so the message is retried, when Temporal is unreachable for a plan.submitted", async () => {
    const { handle, dead } = setup(vi.fn().mockRejectedValue(new Error("temporal unavailable")));
    await expect(handle(message(planSubmitted(), "passage.budget.plan"))).rejects.toThrow("temporal unavailable");
    expect(dead).toEqual([]);
  });
});
