import { fileURLToPath } from "node:url";
import { NativeConnection, Worker } from "@temporalio/worker";
import { createActivities, createPartyHttpClient } from "./activities.js";
import { createPaymentRunActivities } from "./paymentRunActivities.js";

const address = process.env.TEMPORAL_ADDRESS ?? "temporal-frontend.temporal.svc:7233";
const taskQueue = process.env.TEMPORAL_TASK_QUEUE ?? "passage-approvals";
const connection = await NativeConnection.connect({ address });
const worker = await Worker.create({
  connection,
  namespace: process.env.TEMPORAL_NAMESPACE ?? "passage",
  taskQueue,
  activities: {
    ...createActivities(createPartyHttpClient(process.env.PARTY_BASE_URL ?? "http://party.passage.svc")),
    ...createPaymentRunActivities(process.env.PAYABLES_BASE_URL ?? "http://payables.passage.svc"),
  },
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
