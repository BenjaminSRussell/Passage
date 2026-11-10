// Print service queue management (/api/v1/print) for the demo: printers by queue, routing rules, the job
// log and printer status, with edit, move, retire and delete. Typed against the module's own client types.
import type { PrintJob, Printer, PrinterStatus, RoutingRule } from "../../modules/mobile-flows/api/print";
import { reply, route } from "../backend";
import { at, newId, table, uid } from "../store";

const BASE = "/api/v1/print";
const K_PRINTER = 380;
const K_RULE = 381;
const K_JOB = 382;

const printers = table<Printer>([
  { id: uid(K_PRINTER, 1), code: "DOCK-1", name: "Dock door 1 label printer", deviceType: "ZPL_II", dpi: 203, mediaSize: "4X6_IN", host: "10.20.4.21", port: 9100, queueCode: "DOCK", failoverRank: 1, createdAt: at(-120) },
  { id: uid(K_PRINTER, 2), code: "DOCK-2", name: "Dock door 2 label printer", deviceType: "ZPL_II", dpi: 203, mediaSize: "4X6_IN", host: "10.20.4.22", port: 9100, queueCode: "DOCK", failoverRank: 2, createdAt: at(-120) },
  { id: uid(K_PRINTER, 3), code: "PACK-1", name: "Pack bench item labels", deviceType: "ZPL_II", dpi: 300, mediaSize: "2X1_IN", host: "10.20.4.31", port: 9100, queueCode: "PACK", failoverRank: 1, createdAt: at(-90) },
  { id: uid(K_PRINTER, 4), code: "OFFICE-1", name: "Warehouse office", deviceType: "PDF_IPP", dpi: 600, mediaSize: "LETTER", host: "10.20.6.5", port: 631, queueCode: "OFFICE", failoverRank: 1, createdAt: at(-200) },
]);

const rules = table<RoutingRule>([
  { id: uid(K_RULE, 1), documentType: "LP_LABEL", queueCode: "DOCK", sequence: 1 },
  { id: uid(K_RULE, 2), documentType: "ITEM_LABEL", zone: "PACK", queueCode: "PACK", sequence: 1 },
  { id: uid(K_RULE, 3), documentType: "PICK_LIST", queueCode: "OFFICE", sequence: 1 },
]);

const jobs = table<PrintJob>([
  { id: uid(K_JOB, 1), templateCode: "LP_LABEL", queueCode: "DOCK", format: "ZPL", printerId: uid(K_PRINTER, 1), printerCode: "DOCK-1", status: "PRINTED", attempts: 1, createdBy: "Dana Whitfield", createdAt: at(-0.02), updatedAt: at(-0.02) },
  { id: uid(K_JOB, 2), templateCode: "ITEM_LABEL", queueCode: "PACK", format: "ZPL", printerId: uid(K_PRINTER, 3), printerCode: "PACK-1", status: "PRINTED", attempts: 1, createdBy: "Marcus Bell", createdAt: at(-0.1), updatedAt: at(-0.1) },
]);

const active = () => printers.all().filter((p) => p.retiredAt === undefined);

route("GET", `${BASE}/printers`, ({ query }) => {
  const all = printers.all().sort((a, b) => a.code.localeCompare(b.code));
  return { items: query.get("includeRetired") === "true" ? all : all.filter((p) => p.retiredAt === undefined) };
});

route("POST", `${BASE}/printers`, ({ body }) => {
  const b = body as Partial<Printer>;
  if (active().some((p) => p.queueCode === b.queueCode && p.failoverRank === b.failoverRank)) {
    return reply.problem(409, "PRN-0006", "queue already has a printer at this failover rank");
  }
  const device = b.deviceType ?? "ZPL_II";
  return reply.created(
    printers.insert({
      id: newId(),
      code: String(b.code),
      name: String(b.name),
      deviceType: device,
      dpi: b.dpi ?? 203,
      mediaSize: b.mediaSize ?? (device === "ZPL_II" ? "4X6_IN" : "A4"),
      host: String(b.host),
      port: b.port ?? (device === "PDF_IPP" ? 631 : 9100),
      queueCode: String(b.queueCode),
      failoverRank: b.failoverRank ?? 1,
      createdAt: new Date().toISOString(),
    }),
  );
});

route("PATCH", `${BASE}/printers/{id}`, ({ params, body }) => {
  const current = printers.get(params.id);
  if (!current || current.retiredAt) return reply.problem(404, "PRN-0010", "printer not found or already retired");
  const patch = body as Partial<Printer>;
  const next = { ...current, ...patch, id: current.id, code: current.code, deviceType: current.deviceType };
  if (active().some((p) => p.id !== next.id && p.queueCode === next.queueCode && p.failoverRank === next.failoverRank)) {
    return reply.problem(409, "PRN-0006", "queue already has a printer at this failover rank");
  }
  return printers.update(next.id, next);
});

route("POST", `${BASE}/printers/{id}:retire`, ({ params }) => {
  const current = printers.get(params.id);
  if (!current || current.retiredAt) return reply.problem(404, "PRN-0010", "printer not found or already retired");
  return printers.update(current.id, { retiredAt: new Date().toISOString() });
});

route("GET", `${BASE}/printers/status`, () => {
  const queued = (queue: string) => jobs.all().filter((j) => j.queueCode === queue && j.status === "QUEUED").length;
  const items: PrinterStatus[] = active().map((p) => {
    const last = jobs.all().filter((j) => j.printerCode === p.code).sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
    return { printerId: p.id, code: p.code, queueCode: p.queueCode, state: last ? "READY" : "IDLE", queueDepth: queued(p.queueCode), ...(last ? { lastJobAt: last.updatedAt } : {}) };
  });
  return { items };
});

route("GET", `${BASE}/routing-rules`, () => ({ items: rules.all().sort((a, b) => a.sequence - b.sequence) }));

route("POST", `${BASE}/routing-rules`, ({ body }) => reply.created(rules.insert({ ...(body as Omit<RoutingRule, "id">), id: newId() })));

route("PUT", `${BASE}/routing-rules/{id}`, ({ params, body }) => {
  if (!rules.get(params.id)) return reply.problem(404, "PRN-0011", "routing rule not found");
  return rules.update(params.id, { ...(body as Omit<RoutingRule, "id">), id: params.id });
});

route("DELETE", `${BASE}/routing-rules/{id}`, ({ params }) => (rules.remove(params.id) ? reply.noContent() : reply.problem(404, "PRN-0011", "routing rule not found")));

route("GET", `${BASE}/jobs`, ({ query }) => {
  const status = query.get("status");
  const queue = query.get("queueCode");
  return { items: jobs.all().filter((j) => (!status || j.status === status) && (!queue || j.queueCode === queue)).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
});

route("GET", `${BASE}/jobs/{id}`, ({ params }) => jobs.get(params.id) ?? reply.problem(404, "PRN-0005", "print job not found"));
