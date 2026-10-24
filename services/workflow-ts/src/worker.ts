import { fileURLToPath } from "node:url";
import { NativeConnection, Worker } from "@temporalio/worker";

const address = process.env.TEMPORAL_ADDRESS ?? "temporal-frontend.temporal.svc:7233";
const taskQueue = process.env.TEMPORAL_TASK_QUEUE ?? "passage-approvals";
const connection = await NativeConnection.connect({ address });
const worker = await Worker.create({
  connection,
  namespace: process.env.TEMPORAL_NAMESPACE ?? "passage",
  taskQueue,
  workflowsPath: fileURLToPath(new URL("./workflows.js", import.meta.url)),
});

const shutdown = (): void => worker.shutdown();
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

try {
  await worker.run();
} finally {
  await connection.close();
}
