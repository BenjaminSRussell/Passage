import type { IncomingMessage, ServerResponse } from "node:http";
import type { KillSwitchRegistry } from "../domain/killswitch.js";
import { type ApprovalStore, handleApprovalDecision } from "./decision.js";
import { handleKillSwitchCommand, type KillSwitchWiring, readKillSwitchStatus } from "./killswitch.js";
import { COPILOT_DUTY, type CopilotService } from "../runtime/copilot.js";
import { parseChunks, parseSearch, type KnowledgeService } from "../runtime/knowledge.js";
import type { RunService } from "../runtime/service.js";
import { handleChat } from "./copilot.js";
import type { Authenticate } from "./auth.js";
import { ApiError, requireDuty, toApiError } from "./principal.js";
import { bearerToken, handleGetRun, handleListActions, handleStartRun } from "./runs.js";

/** 64 KiB is far above any control payload in this API. */
export const MAX_BODY_BYTES = 64 * 1024;

export type AgentsApiDeps = AgentsApiBase & KillSwitchWiring;

export interface AgentsApiBase {
  /** Who the caller is: the verified bearer token (auth.ts createAuthenticator), never the edge headers alone. */
  authenticate: Authenticate;
  registry: KillSwitchRegistry;
  approvals: ApprovalStore;
  /** Run start and read endpoints; absent in a process that serves only the control routes. */
  runs?: RunService;
  /** The copilot chat endpoint. */
  copilot?: CopilotService;
  /** The knowledge index and its hybrid search; absent and the knowledge routes answer 404. */
  knowledge?: KnowledgeService;
  now?: () => Date;
}

const UUID = "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}";
const KILL_PATH = "/api/v1/agents/kill-switch";
const START_PATH = /^\/api\/v1\/agents\/([a-z][a-z0-9_]{0,63})\/runs$/;
const RUN_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})$`);
const ACTIONS_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})/actions$`);
const CHAT_PATH = "/api/v1/copilot/chat";
const KNOWLEDGE_CHUNKS_PATH = "/api/v1/knowledge/chunks";
const KNOWLEDGE_SEARCH_PATH = "/api/v1/knowledge/search";
const DECISION_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})/actions/(${UUID})/decision$`);

async function readBody(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const buf = typeof chunk === "string" ? Buffer.from(chunk) : (chunk as Buffer);
    size += buf.length;
    if (size > MAX_BODY_BYTES) throw new ApiError(413, "AGT-0413", "request body exceeds 64 KiB");
    chunks.push(buf);
  }
  if (size === 0) throw new ApiError(400, "AGT-0400", "a JSON body is required");
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8")) as unknown;
  } catch {
    throw new ApiError(400, "AGT-0400", "body is not valid JSON");
  }
}

function send(res: ServerResponse, status: number, body: unknown): void {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(payload).toString(),
  });
  res.end(payload);
}

const TITLES: Readonly<Record<number, string>> = {
  400: "Bad Request",
  401: "Unauthorized",
  403: "Forbidden",
  404: "Not Found",
  405: "Method Not Allowed",
  409: "Conflict",
  413: "Content Too Large",
  422: "Unprocessable Content",
  500: "Internal Server Error",
  503: "Service Unavailable",
};

/** RFC 9457 problem details in the kernel's shape: type, title, status, code, detail. */
function problem(res: ServerResponse, err: ApiError, path: string, extra: Record<string, string> = {}): void {
  const payload = JSON.stringify({
    type: `https://passage.dev/problems/${err.code}`,
    title: TITLES[err.status] ?? "Error",
    status: err.status,
    code: err.code,
    detail: err.message,
    instance: path,
  });
  res.writeHead(err.status, {
    "content-type": "application/problem+json",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(payload).toString(),
    "x-content-type-options": "nosniff",
    ...extra,
  });
  res.end(payload);
}

class MethodNotAllowed extends ApiError {
  readonly allow: string;

  constructor(allow: string) {
    super(405, "AGT-0405", "method not allowed");
    this.allow = allow;
  }
}

/**
 * The agents HTTP surface served in this slice: the kill switch (read and
 * toggle) and the approval decision. Every other route answers 404. A toggle's
 * event is published before the 200 is sent.
 */
export function createAgentsHandler(deps: AgentsApiDeps): (req: IncomingMessage, res: ServerResponse) => Promise<void> {
  const now = deps.now ?? (() => new Date());
  return async (req, res) => {
    const path = new URL(req.url ?? "/", "http://agents.internal").pathname;
    try {
      if (path === KILL_PATH) {
        if (req.method === "GET") {
          send(res, 200, readKillSwitchStatus(await deps.authenticate(req.headers), deps.registry));
          return;
        }
        if (req.method !== "POST") throw new MethodNotAllowed("GET, POST");
        const principal = await deps.authenticate(req.headers);
        const out = await handleKillSwitchCommand({
          principal,
          body: await readBody(req),
          registry: deps.registry,
          now,
          ...("log" in deps ? { log: deps.log } : { versions: deps.versions, publish: deps.publish }),
        });
        send(res, 200, out.body);
        return;
      }
      if (path === CHAT_PATH) {
        if (deps.copilot === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        await handleChat({ principal, token: bearerToken(req.headers), body: await readBody(req), accept: String(req.headers["accept"] ?? ""), copilot: deps.copilot, res });
        return;
      }
      if (path === KNOWLEDGE_CHUNKS_PATH || path === KNOWLEDGE_SEARCH_PATH) {
        if (deps.knowledge === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        if (path === KNOWLEDGE_CHUNKS_PATH) {
          requireDuty(principal, "knowledge.write", "the knowledge index");
          send(res, 200, await deps.knowledge.index(principal.tenantId, parseChunks(await readBody(req))));
        } else {
          requireDuty(principal, COPILOT_DUTY, "the knowledge search");
          const q = parseSearch(await readBody(req));
          send(res, 200, await deps.knowledge.search(principal.tenantId, q.query, q.limit));
        }
        return;
      }
      const start = START_PATH.exec(path);
      if (start !== null && deps.runs !== undefined) {
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        const started = await handleStartRun({ principal, token: bearerToken(req.headers), agentId: start[1] ?? "", body: await readBody(req), runs: deps.runs });
        send(res, 202, started);
        return;
      }
      const runMatch = RUN_PATH.exec(path) ?? ACTIONS_PATH.exec(path);
      if (runMatch !== null && deps.runs !== undefined) {
        if (req.method !== "GET") throw new MethodNotAllowed("GET");
        const principal = await deps.authenticate(req.headers);
        const runId = (runMatch[1] ?? "").toLowerCase();
        send(res, 200, path.endsWith("/actions") ? await handleListActions({ principal, runId, runs: deps.runs }) : await handleGetRun({ principal, runId, runs: deps.runs }));
        return;
      }
      const match = DECISION_PATH.exec(path);
      if (match === null) throw new ApiError(404, "AGT-0404", "no such route");
      if (req.method !== "POST") throw new MethodNotAllowed("POST");
      const principal = await deps.authenticate(req.headers);
      const out = await handleApprovalDecision({
        principal,
        runId: (match[1] ?? "").toLowerCase(),
        actionId: (match[2] ?? "").toLowerCase(),
        body: await readBody(req),
        store: deps.approvals,
        now,
      });
      send(res, 200, out);
    } catch (err) {
      // Internal errors are never echoed to the caller.
      if (res.headersSent) {
        res.destroy();
        return;
      }
      const apiErr = toApiError(err);
      problem(res, apiErr, path, err instanceof MethodNotAllowed ? { allow: err.allow } : {});
    }
  };
}
