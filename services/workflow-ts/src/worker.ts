import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { Client, Connection } from "@temporalio/client";
import { NativeConnection, Worker } from "@temporalio/worker";
import { createDecisionHandler } from "./decisionApi.js";
import { buildActivities } from "./activityRegistry.js";

const address = process.env.TEMPORAL_ADDRESS ?? "temporal-frontend.temporal.svc:7233";
const taskQueue = process.env.TEMPORAL_TASK_QUEUE ?? "passage-approvals";
const connection = await NativeConnection.connect({ address });
const worker = await Worker.create({
  connection,
  namespace: process.env.TEMPORAL_NAMESPACE ?? "passage",
  taskQueue,
  activities: buildActivities(
    process.env,
    (path) => readFileSync(path, "utf8"),
    (message, attributes) => process.stdout.write(`${JSON.stringify({ level: "warn", message, ...attributes })}\n`),
  ),
  workflowsPath: fileURLToPath(new URL("./workflows.js", import.meta.url)),
});

const clientConnection = await Connection.connect({ address });
const client = new Client({ connection: clientConnection, namespace: process.env.TEMPORAL_NAMESPACE ?? "passage" });
const decisions = createServer(createDecisionHandler(client)).listen(Number(process.env.HTTP_PORT ?? 8080));

const shutdown = (): void => {
  decisions.close();
  worker.shutdown();
};
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

try {
  await worker.run();
} finally {
  await clientConnection.close();
  await connection.close();
}
