// The /v1/carrier HTTP API (api/openapi.yaml). Every /v1 route needs a signed service token naming the route's duty;
// there is no person-facing route: people reach carriers through fulfillment and transport, which call here.

import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { Gateway, GatewayError } from "./gateway.ts";
import { DUTY_LABEL, DUTY_RATE, DUTY_TRACK, HEADER, ServiceTokenError, type ServiceClaims, type ServiceTokenVerifier } from "./serviceToken.ts";
import { code, codes, shipment, type FieldErrors } from "./validate.ts";

const MAX_BODY = 256 * 1024;
const IDEMPOTENCY_KEY = /^[\x21-\x7e]{1,128}$/;
const TRACKING_PATH = /^\/v1\/carrier\/tracking\/([a-z0-9-]{2,32})\/([A-Za-z0-9-]{1,64})$/;

interface Problem {
  status: number;
  code: string;
  detail: string;
  errors?: FieldErrors;
}

const TITLES: Record<number, string> = {
  400: "Bad Request",
  401: "Unauthorized",
  403: "Forbidden",
  404: "Not Found",
  405: "Method Not Allowed",
  413: "Payload Too Large",
  415: "Unsupported Media Type",
  422: "Unprocessable Entity",
  428: "Precondition Required",
  500: "Internal Server Error",
  503: "Service Unavailable",
};

function send(res: ServerResponse, status: number, body: unknown, contentType = "application/json"): void {
  const raw = JSON.stringify(body);
  res.writeHead(status, { "content-type": contentType, "content-length": Buffer.byteLength(raw), "cache-control": "no-store" });
  res.end(raw);
}

function problem(res: ServerResponse, p: Problem, path: string): void {
  send(
    res,
    p.status,
    { type: "about:blank", title: TITLES[p.status] ?? "Error", status: p.status, code: p.code, detail: p.detail, instance: path, ...(p.errors ? { errors: p.errors } : {}) },
    "application/problem+json",
  );
}

class HttpProblem extends Error {
  readonly p: Problem;
  constructor(p: Problem) {
    super(p.detail);
    this.p = p;
  }
}

const invalid = (errors: FieldErrors): HttpProblem => new HttpProblem({ status: 400, code: "CGW-0400", detail: "the request is not valid", errors });

async function readJson(req: IncomingMessage): Promise<Record<string, unknown>> {
  if (!(req.headers["content-type"] ?? "").toLowerCase().startsWith("application/json")) {
    throw new HttpProblem({ status: 415, code: "CGW-0415", detail: "the body must be application/json" });
  }
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += (chunk as Buffer).length;
    if (size > MAX_BODY) throw new HttpProblem({ status: 413, code: "CGW-0413", detail: "the body is larger than 256 KiB" });
    chunks.push(chunk as Buffer);
  }
  let body: unknown;
  try {
    body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw invalid({ body: "is not JSON" });
  }
  if (typeof body !== "object" || body === null || Array.isArray(body)) throw invalid({ body: "must be a JSON object" });
  return body as Record<string, unknown>;
}

export interface ServerOptions {
  gateway: Gateway;
  verifier: ServiceTokenVerifier;
  log?: (msg: string, fields: Record<string, unknown>) => void;
}

export function createApp(options: ServerOptions): (req: IncomingMessage, res: ServerResponse) => Promise<void> {
  const { gateway, verifier } = options;
  const log = options.log ?? ((msg, fields) => console.log(JSON.stringify({ msg, ...fields })));

  function authorize(req: IncomingMessage, duty: string): ServiceClaims {
    const token = req.headers[HEADER];
    if (typeof token !== "string" || token === "") throw new HttpProblem({ status: 401, code: "CGW-0401", detail: "a service token is required" });
    let claims: ServiceClaims;
    try {
      claims = verifier.verify(token);
    } catch (err) {
      if (err instanceof ServiceTokenError) throw new HttpProblem({ status: 401, code: "CGW-0401", detail: "the service token is not valid" });
      throw err;
    }
    if (!claims.duties.includes(duty)) throw new HttpProblem({ status: 403, code: "CGW-0403", detail: `the service token does not carry ${duty}` });
    return claims;
  }

  async function route(req: IncomingMessage, res: ServerResponse, path: string): Promise<void> {
    const method = req.method ?? "GET";
    if (path === "/healthz" || path === "/readyz") {
      send(res, 200, { status: "ok" });
      return;
    }
    if (path === "/v1/carrier/carriers") {
      if (method !== "GET") throw new HttpProblem({ status: 405, code: "CGW-0405", detail: "use GET" });
      authorize(req, DUTY_RATE);
      send(res, 200, { items: gateway.carriers() });
      return;
    }
    if (path === "/v1/carrier/rates") {
      if (method !== "POST") throw new HttpProblem({ status: 405, code: "CGW-0405", detail: "use POST" });
      authorize(req, DUTY_RATE);
      const body = await readJson(req);
      const errors: FieldErrors = {};
      const carriers = codes(errors, "carriers", body["carriers"]);
      const services = codes(errors, "services", body["services"]);
      const s = shipment(errors, body);
      if (!s || Object.keys(errors).length > 0) throw invalid(errors);
      const result = await gateway.rateShop({ ...s, ...(services ? { services } : {}) }, carriers);
      send(res, 200, result);
      return;
    }
    if (path === "/v1/carrier/labels") {
      if (method !== "POST") throw new HttpProblem({ status: 405, code: "CGW-0405", detail: "use POST" });
      const claims = authorize(req, DUTY_LABEL);
      const key = req.headers["idempotency-key"];
      if (typeof key !== "string" || !IDEMPOTENCY_KEY.test(key)) {
        throw new HttpProblem({ status: 428, code: "CGW-0428", detail: "Idempotency-Key (1 to 128 visible ASCII characters) is required" });
      }
      const body = await readJson(req);
      const errors: FieldErrors = {};
      const carrier = code(errors, "carrier", body["carrier"]);
      const service = code(errors, "service", body["service"]);
      const reference = body["reference"];
      if (reference !== undefined && (typeof reference !== "string" || reference.length > 60)) errors["reference"] = "must be at most 60 characters";
      const s = shipment(errors, body);
      if (!s || !carrier || !service || Object.keys(errors).length > 0) throw invalid(errors);
      const { label, created } = await gateway.label({ tenantId: claims.tid, legalEntityId: claims.le, issuer: claims.iss }, carrier, {
        ...s,
        service,
        idempotencyKey: key,
        ...(typeof reference === "string" ? { reference } : {}),
      });
      log("carrier label", { tenantId: claims.tid, legalEntityId: claims.le, issuer: claims.iss, actor: claims.act, carrier, trackingNumber: label.trackingNumber, created });
      send(res, created ? 201 : 200, { ...label, created });
      return;
    }
    const m = TRACKING_PATH.exec(path);
    if (m) {
      if (method !== "GET") throw new HttpProblem({ status: 405, code: "CGW-0405", detail: "use GET" });
      authorize(req, DUTY_TRACK);
      send(res, 200, await gateway.track(m[1]!, m[2]!));
      return;
    }
    throw new HttpProblem({ status: 404, code: "CGW-0404", detail: "no such route" });
  }

  return async (req, res) => {
    const path = (req.url ?? "/").split("?")[0]!;
    try {
      await route(req, res, path);
    } catch (err) {
      if (err instanceof HttpProblem) problem(res, err.p, path);
      else if (err instanceof GatewayError) problem(res, { status: err.status, code: err.code, detail: err.message }, path);
      else {
        log("carrier gateway error", { path, err: String(err) });
        problem(res, { status: 500, code: "CGW-0500", detail: "the request could not be completed" }, path);
      }
    }
  };
}

export function createHttpServer(options: ServerOptions): Server {
  const app = createApp(options);
  return createServer((req, res) => void app(req, res));
}
