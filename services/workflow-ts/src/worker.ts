import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { Client, Connection } from "@temporalio/client";
import { NativeConnection, Worker } from "@temporalio/worker";
import { createDecisionHandler } from "./decisionApi.js";
import { buildActivities } from "./activityRegistry.js";
import { eventConsumerConfigFromEnv, startEventConsumer, type RunningEventConsumer } from "./eventConsumer.js";

const address = process.env.TEMPORAL_ADDRESS ?? "temporal-frontend.temporal.svc:7233";
const taskQueue = process.env.TEMPORAL_TASK_QUEUE ?? "passage-approvals";
const warn = (message: string, attributes: Record<string, string>): void => {
  process.stdout.write(`${JSON.stringify({ level: "warn", message, ...attributes })}\n`);
};
const connection = await NativeConnection.connect({ address });
const worker = await Worker.create({
  connection,
  namespace: process.env.TEMPORAL_NAMESPACE ?? "passage",
  taskQueue,
  activities: buildActivities(process.env, (path) => readFileSync(path, "utf8"), warn),
  workflowsPath: fileURLToPath(new URL("./workflows.js", import.meta.url)),
});

const clientConnection = await Connection.connect({ address });
const client = new Client({ connection: clientConnection, namespace: process.env.TEMPORAL_NAMESPACE ?? "passage" });
const decisions = createServer(createDecisionHandler(client)).listen(Number(process.env.HTTP_PORT ?? 8080));
// Starts tenderWorkflow on transport's load.released and signals it on tender.accepted and tender.declined (#426).
const consumerConfig = eventConsumerConfigFromEnv(process.env);
const events: RunningEventConsumer | null = consumerConfig ? await startEventConsumer(consumerConfig, client, taskQueue, warn) : null;

const shutdown = (): void => {
  decisions.close();
  worker.shutdown();
};
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

try {
  await worker.run();
} finally {
  // The consumer stops before the Temporal client it starts and signals through.
  await events?.stop();
  await clientConnection.close();
  await connection.close();
}
