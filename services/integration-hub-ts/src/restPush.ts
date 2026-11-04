// Generic REST push to a customer's existing ERP (FR-35.9, integration track S7).
//
// A `rest-push` subscription turns each matching event into one HTTP request to the legacy system:
//   1. the migration service renders the event through the subscription's outbound mapping
//      ({body, idempotencyKey, refs}; MigrationClient);
//   2. the URL template's {Placeholders} are filled from the rendered body (path segments escaped);
//   3. the request goes out with the configured auth and `Idempotency-Key` = the event id, so a retry,
//      a resend after a crash and a replay never create a second record at the receiver;
//   4. on 2xx the legacy key at `responseKeyPath` is recorded as the cross-reference (xref:record) and the
//      delivery is done.
// Outcomes are classified into RestOutcome; the Dispatcher applies the schedule (src/dispatch.ts restStep).
// Credentials are opened only here, only for the request, and are removed from anything kept or logged.

import { createHash } from "node:crypto";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";

import type { HubEvent, PendingDelivery, RestOutcome } from "./dispatcher.ts";
import { CODE_XREF_EVENT_CONFLICT, cloudEventOf, elementRefs, type LookupUse, type MigrationClient } from "./migrationClient.ts";
import { validateWebhookUrl, type GuardOptions, type PinnedTarget } from "./ssrf.ts";

export const METHODS = ["POST", "PUT", "PATCH"] as const;
export type RestMethod = (typeof METHODS)[number];
export const AUTH_TYPES = ["basic", "bearer", "api-key", "oauth2-client-credentials"] as const;
export type AuthType = (typeof AUTH_TYPES)[number];
export type ClientErrorPolicy = "dead_letter" | "retry";

/** The part of a rest-push subscription that is safe to store plain and return from the API. */
export interface RestConfig {
  outboundMappingId: string;
  method: RestMethod;
  /** JSONPath-lite to the legacy key in the response body ("$.data.id", "orders[0].id"); empty when the receiver returns none. */
  responseKeyPath: string;
  /**
   * Per-element key path for a push that carries a collection, with exactly one `[*]` ("lines[*].id", "$.result.lines[*].no"):
   * the legacy key the ERP assigned to each element. Matched to the pushed elements by position, or by `lineCorrelation`.
   * Empty when the receiver returns none (or the push has no collection). Needs `responseKeyPath` (the header's key).
   */
  lineKeyPath: string;
  /** Matches response elements to pushed ones by a field instead of by position: both paths have one `[*]`. */
  lineCorrelation?: { bodyPath: string; responsePath: string };
  /** What a 4xx other than 410 and 429 does: dead-letter at once (default) or follow the retry schedule. */
  onClientError: ClientErrorPolicy;
  auth: {
    type: AuthType;
    username?: string;
    headerName?: string;
    tokenUrl?: string;
    clientId?: string;
    scope?: string;
  };
}

/** The write-only part, sealed at rest. */
export interface RestCredentials {
  password?: string;
  token?: string;
  value?: string;
  clientSecret?: string;
}

/** The legacy key of one element of a pushed collection. */
export interface LineKey {
  /** Position of the element in the pushed collection (from 0). */
  index: number;
  legacyKey: string;
}

export class RestConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "RestConfigError";
  }
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
/** Header names the hub sets itself; an api-key header may not replace them. */
const RESERVED_HEADERS = new Set(["host", "content-length", "content-type", "idempotency-key", "authorization", "transfer-encoding", "connection", "accept"]);
const HEADER_NAME = /^[A-Za-z0-9-]{1,64}$/;
const MAX_SECRET = 4096;

function str(v: unknown, what: string, max: number, optional = false): string | undefined {
  if (v === undefined && optional) return undefined;
  if (typeof v !== "string" || v.trim() === "" || v.length > max) {
    throw new RestConfigError(`${what} must be a non-empty string of at most ${max} characters`);
  }
  return v;
}

// ---- JSONPath-lite -------------------------------------------------------------------------------------------

type Step = string | number;

/** Parses "$.a.b[0].c" or "a.b[0].c": dotted names and [index] only. Throws RestConfigError. */
export function parsePath(path: string): Step[] {
  const steps: Step[] = [];
  let rest = path.startsWith("$") ? path.slice(1) : path;
  if (rest === "") throw new RestConfigError("a path must name at least one field");
  if (!rest.startsWith(".") && !rest.startsWith("[")) rest = "." + rest;
  const token = /^(?:\.([A-Za-z_][A-Za-z0-9_-]*)|\[(\d{1,6})\])/;
  while (rest !== "") {
    const m = token.exec(rest);
    if (m === null) throw new RestConfigError(`path ${JSON.stringify(path)} is not valid JSONPath-lite (use $.field.field[0])`);
    steps.push(m[1] !== undefined ? m[1] : Number(m[2]));
    rest = rest.slice(m[0].length);
  }
  return steps;
}

/** The value at the path, or undefined. */
export function readPath(value: unknown, path: string): unknown {
  let cur: unknown = value;
  for (const step of parsePath(path)) {
    if (typeof step === "number") {
      if (!Array.isArray(cur)) return undefined;
      cur = cur[step];
    } else {
      if (typeof cur !== "object" || cur === null || Array.isArray(cur) || !Object.hasOwn(cur, step)) return undefined;
      cur = (cur as Record<string, unknown>)[step];
    }
  }
  return cur;
}

/** A path with exactly one `[*]`: the array's path and the path inside each element ("" for the element itself). */
export interface ElementPath {
  array: string;
  element: string;
}

export function parseElementPath(path: string): ElementPath {
  const parts = path.split("[*]");
  if (parts.length !== 2) throw new RestConfigError(`path ${JSON.stringify(path)} needs exactly one [*] (for example lines[*].id)`);
  const [array, element] = parts as [string, string];
  if (element !== "" && !element.startsWith(".") && !element.startsWith("[")) throw new RestConfigError(`path ${JSON.stringify(path)} is not valid after [*] (use lines[*].id)`);
  if (array !== "" && array !== "$") parsePath(array);
  if (element !== "") parsePath("$" + element);
  return { array, element };
}

/** The values at the element path of every element of the array, or undefined when the array is missing. */
export function readElements(value: unknown, path: string): unknown[] | undefined {
  const { array, element } = parseElementPath(path);
  const arr = array === "" || array === "$" ? value : readPath(value, array);
  if (!Array.isArray(arr)) return undefined;
  return arr.map((e) => (element === "" ? e : readPath(e, "$" + element)));
}

const keyText = (v: unknown): string | undefined => (typeof v === "number" ? String(v) : typeof v === "string" && v !== "" ? v : undefined);

export type LineKeyResult = { ok: true; keys: LineKey[] } | { ok: false; reason: string };

/**
 * The legacy keys of a pushed collection's elements from the receiver's answer. By position unless
 * `config.lineCorrelation` names a field: the response element carrying the value the pushed element carried.
 * `expected` is how many elements the render named refs for (null when it named none); a different count is a failure,
 * because a key given to the wrong line would be worse than none.
 */
export function matchLineKeys(config: RestConfig, pushed: unknown, response: unknown, expected: number | null): LineKeyResult {
  const raw = readElements(response, config.lineKeyPath);
  if (raw === undefined) return { ok: false, reason: `no array at ${config.lineKeyPath}` };
  const texts = raw.map(keyText);
  const bad = texts.findIndex((t) => t === undefined);
  if (bad >= 0) return { ok: false, reason: `no legacy key at ${config.lineKeyPath} for response element ${bad}` };
  const keys = texts as string[];
  const c = config.lineCorrelation;
  if (c === undefined) {
    if (expected !== null && keys.length !== expected) return { ok: false, reason: `${keys.length} line keys for ${expected} pushed elements` };
    return { ok: true, keys: keys.map((legacyKey, index) => ({ index, legacyKey })) };
  }
  const bodyVals = readElements(pushed, c.bodyPath);
  const respVals = readElements(response, c.responsePath);
  if (bodyVals === undefined) return { ok: false, reason: `no array at ${c.bodyPath} in the pushed body` };
  if (respVals === undefined || respVals.length !== keys.length) return { ok: false, reason: `the response has no ${c.responsePath} for each element at ${config.lineKeyPath}` };
  if (expected !== null && bodyVals.length !== expected) return { ok: false, reason: `${bodyVals.length} pushed elements at ${c.bodyPath} for ${expected} refs` };
  // Every pushed element needs its key, as by position: a line left without one would be created again by its next event.
  if (keys.length !== bodyVals.length) return { ok: false, reason: `${keys.length} line keys for ${bodyVals.length} pushed elements at ${c.bodyPath}` };
  const byValue = new Map<string, number>();
  for (let i = 0; i < bodyVals.length; i++) {
    const v = keyText(bodyVals[i]);
    if (v === undefined || byValue.has(v)) return { ok: false, reason: `pushed element ${i} has no unique value at ${c.bodyPath}` };
    byValue.set(v, i);
  }
  const used = new Set<number>();
  const out: LineKey[] = [];
  for (let j = 0; j < keys.length; j++) {
    const v = keyText(respVals[j]);
    const index = v === undefined ? undefined : byValue.get(v);
    if (index === undefined || used.has(index)) return { ok: false, reason: `response element ${j} matches no pushed element by ${c.responsePath}` };
    used.add(index);
    out.push({ index, legacyKey: keys[j] as string });
  }
  return { ok: true, keys: out.sort((a, b) => a.index - b.index) };
}

/** The body array a lineCorrelation.bodyPath iterates, as the migration service names a collection (Lines, Header.Lines). */
export function bodyArrayOf(config: RestConfig): string | undefined {
  if (config.lineCorrelation === undefined) return undefined;
  const array = parseElementPath(config.lineCorrelation.bodyPath).array;
  return array.startsWith("$.") ? array.slice(2) : array.startsWith("$") ? array.slice(1) : array.startsWith(".") ? array.slice(1) : array;
}

/**
 * The line keys of one accepted push: matched against the render's element refs of the collection they belong to.
 * `collection` is undefined when the render named no element refs (the keys then stay on the delivery alone).
 */
export function lineKeysOf(
  config: RestConfig,
  pushed: unknown,
  response: unknown,
  refs: unknown,
): { ok: true; keys: LineKey[]; collection?: string | null } | { ok: false; reason: string } {
  const els = elementRefs(refs, bodyArrayOf(config));
  if (els !== null && !els.ok) return els;
  const m = matchLineKeys(config, pushed, response, els === null ? null : els.count);
  if (!m.ok) return m;
  return els === null ? m : { ...m, collection: els.collection };
}

// ---- URL template --------------------------------------------------------------------------------------------

const PLACEHOLDER = /\{([^{}]*)\}/g;

export class TemplateError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TemplateError";
  }
}

export function placeholders(template: string): string[] {
  return [...template.matchAll(PLACEHOLDER)].map((m) => m[1] as string);
}

/**
 * Fills the template from the rendered body. A placeholder is a path into the body ("CustAccount",
 * "$.header.account", "lines[0].no") whose value must be a string, number or boolean; it is escaped as one
 * path segment or query value, so a value cannot add a segment, a query or a fragment. The host never
 * takes a placeholder (checked at create time).
 */
export function expandUrl(template: string, body: unknown): string {
  return template.replace(PLACEHOLDER, (_all, name: string) => {
    let value: unknown;
    try {
      value = readPath(body, name);
    } catch {
      throw new TemplateError(`placeholder {${name}} is not a valid path`);
    }
    if (typeof value === "number" || typeof value === "boolean") value = String(value);
    if (typeof value !== "string" || value === "") {
      throw new TemplateError(`placeholder {${name}} has no value in the rendered body`);
    }
    if (value === "." || value === "..") {
      throw new TemplateError(`placeholder {${name}} resolves to a path traversal segment`);
    }
    return encodeURIComponent(value);
  });
}

function checkTemplate(template: string): void {
  const idx = template.indexOf("://");
  const afterOrigin = idx < 0 ? -1 : template.indexOf("/", idx + 3);
  const origin = afterOrigin < 0 ? template : template.slice(0, afterOrigin);
  if (origin.includes("{") || origin.includes("}")) {
    throw new RestConfigError("placeholders are allowed in the path and query only, never in the scheme, host or port");
  }
  if (template.includes("#")) throw new RestConfigError("the URL template must not carry a fragment");
  const opens = (template.match(/\{/g) ?? []).length;
  const closes = (template.match(/\}/g) ?? []).length;
  if (opens !== closes || template.replace(PLACEHOLDER, "").match(/[{}]/) !== null) {
    throw new RestConfigError("the URL template has an unbalanced or nested brace");
  }
  for (const name of placeholders(template)) {
    parsePath(name);
  }
}

/** The template with every placeholder replaced by a sample, for the create-time address check. */
export function sampleUrl(template: string): string {
  return template.replace(PLACEHOLDER, "x");
}

// ---- config parsing ------------------------------------------------------------------------------------------

export interface RestInput {
  outboundMappingId: string;
  method: RestMethod;
  responseKeyPath: string;
  lineKeyPath: string;
  lineCorrelation?: { bodyPath: string; responsePath: string };
  onClientError: ClientErrorPolicy;
  auth: RestConfig["auth"];
  credentials: RestCredentials;
}

export const REST_FIELDS = ["outboundMappingId", "method", "responseKeyPath", "lineKeyPath", "lineCorrelation", "onClientError", "auth"] as const;

/** Validates the rest-push fields of a create body (all required but responseKeyPath and onClientError). */
export function parseRestInput(body: Record<string, unknown>, urlTemplate: string): RestInput {
  checkTemplate(urlTemplate);
  const mapping = str(body["outboundMappingId"], "outboundMappingId", 36) as string;
  if (!UUID.test(mapping)) throw new RestConfigError("outboundMappingId must be a UUID");
  const method = (body["method"] ?? "POST") as string;
  if (!(METHODS as readonly string[]).includes(method)) throw new RestConfigError(`method must be one of ${METHODS.join(", ")}`);
  const responseKeyPath = body["responseKeyPath"] === undefined || body["responseKeyPath"] === "" ? "" : (str(body["responseKeyPath"], "responseKeyPath", 200) as string);
  if (responseKeyPath !== "") parsePath(responseKeyPath);
  const lineKeyPath = body["lineKeyPath"] === undefined || body["lineKeyPath"] === "" ? "" : (str(body["lineKeyPath"], "lineKeyPath", 200) as string);
  if (lineKeyPath !== "") {
    parseElementPath(lineKeyPath);
    if (responseKeyPath === "") throw new RestConfigError("lineKeyPath needs responseKeyPath: the header's legacy key is recorded with its lines'");
  }
  let lineCorrelation: { bodyPath: string; responsePath: string } | undefined;
  const lc = body["lineCorrelation"];
  if (lc !== undefined && lc !== null) {
    if (lineKeyPath === "") throw new RestConfigError("lineCorrelation needs lineKeyPath");
    if (typeof lc !== "object" || Array.isArray(lc)) throw new RestConfigError("lineCorrelation must be an object with bodyPath and responsePath");
    const o = lc as Record<string, unknown>;
    for (const k of Object.keys(o)) if (k !== "bodyPath" && k !== "responsePath") throw new RestConfigError(`lineCorrelation.${k} is not a setting`);
    const bodyPath = str(o["bodyPath"], "lineCorrelation.bodyPath", 200) as string;
    const responsePath = str(o["responsePath"], "lineCorrelation.responsePath", 200) as string;
    parseElementPath(bodyPath);
    if (parseElementPath(responsePath).array !== parseElementPath(lineKeyPath).array) throw new RestConfigError("lineCorrelation.responsePath must iterate the same array as lineKeyPath");
    lineCorrelation = { bodyPath, responsePath };
  }
  const onClientError = (body["onClientError"] ?? "dead_letter") as string;
  if (onClientError !== "dead_letter" && onClientError !== "retry") throw new RestConfigError("onClientError must be dead_letter or retry");
  const { auth, credentials } = parseAuth(body["auth"]);
  return { outboundMappingId: mapping.toLowerCase(), method: method as RestMethod, responseKeyPath, lineKeyPath, ...(lineCorrelation === undefined ? {} : { lineCorrelation }), onClientError, auth, credentials };
}

/** Validates a PATCH of a rest-push subscription against its current settings; credentials change only when `auth` is sent. */
export function parseRestPatch(
  old: RestConfig,
  urlTemplate: string,
  body: Record<string, unknown>,
): { config: RestConfig; credentials?: RestCredentials } | null {
  const touched = REST_FIELDS.some((f) => body[f] !== undefined);
  checkTemplate(urlTemplate);
  if (!touched) return null;
  const merged = parseRestInput(
    {
      outboundMappingId: body["outboundMappingId"] ?? old.outboundMappingId,
      method: body["method"] ?? old.method,
      responseKeyPath: body["responseKeyPath"] ?? old.responseKeyPath,
      lineKeyPath: body["lineKeyPath"] ?? old.lineKeyPath ?? "",
      lineCorrelation: body["lineCorrelation"] === undefined ? old.lineCorrelation : body["lineCorrelation"],
      onClientError: body["onClientError"] ?? old.onClientError,
      // Placeholder credentials stand in when `auth` is not being replaced; they are discarded below.
      auth: body["auth"] ?? { type: "bearer", token: "unchanged" },
    },
    urlTemplate,
  );
  if (body["auth"] === undefined) {
    return { config: { ...merged, auth: old.auth } };
  }
  const { credentials, ...config } = merged;
  return { config, credentials };
}

export function parseAuth(raw: unknown): { auth: RestConfig["auth"]; credentials: RestCredentials } {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) throw new RestConfigError("auth must be an object");
  const a = raw as Record<string, unknown>;
  const type = a["type"] as AuthType;
  const only = (allowed: string[]) => {
    const extra = Object.keys(a).find((k) => k !== "type" && !allowed.includes(k));
    if (extra !== undefined) throw new RestConfigError(`auth: unknown field ${extra} for ${type}`);
  };
  switch (type) {
    case "basic":
      only(["username", "password"]);
      return {
        auth: { type, username: str(a["username"], "auth.username", 256) as string },
        credentials: { password: str(a["password"], "auth.password", MAX_SECRET) as string },
      };
    case "bearer":
      only(["token"]);
      return { auth: { type }, credentials: { token: str(a["token"], "auth.token", MAX_SECRET) as string } };
    case "api-key": {
      only(["headerName", "value"]);
      const headerName = str(a["headerName"], "auth.headerName", 64) as string;
      if (!HEADER_NAME.test(headerName) || RESERVED_HEADERS.has(headerName.toLowerCase())) {
        throw new RestConfigError("auth.headerName must be a plain header name the hub does not set itself");
      }
      const value = str(a["value"], "auth.value", MAX_SECRET) as string;
      if (/[\r\n]/.test(value)) throw new RestConfigError("auth.value must not contain line breaks");
      return { auth: { type, headerName }, credentials: { value } };
    }
    case "oauth2-client-credentials": {
      only(["tokenUrl", "clientId", "clientSecret", "scope"]);
      const tokenUrl = str(a["tokenUrl"], "auth.tokenUrl", 2048) as string;
      const scope = str(a["scope"], "auth.scope", 500, true);
      return {
        auth: { type, tokenUrl, clientId: str(a["clientId"], "auth.clientId", 256) as string, ...(scope === undefined ? {} : { scope }) },
        credentials: { clientSecret: str(a["clientSecret"], "auth.clientSecret", MAX_SECRET) as string },
      };
    }
    default:
      throw new RestConfigError(`auth.type must be one of ${AUTH_TYPES.join(", ")}`);
  }
}

export function checkUrlTemplate(template: string): void {
  checkTemplate(template);
}

// ---- HTTP exchange -------------------------------------------------------------------------------------------

export interface ExchangeRequest {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: string;
}
export interface ExchangeResponse {
  status: number;
  headers: Record<string, string | undefined>;
  /** At most RESPONSE_CAP_BYTES of the body. */
  body: string;
}
/** One request/response; null when nothing answered (rejected URL, connection error, timeout). */
export type Exchange = (request: ExchangeRequest) => Promise<ExchangeResponse | null>;

export const RESPONSE_CAP_BYTES = 64 * 1024;

export interface HttpExchangeOptions extends GuardOptions {
  timeoutMs?: number;
  validate?: (url: string) => Promise<PinnedTarget>;
}

/** Like httpTransport (guard on every attempt, connection pinned to the checked address, no redirects) but returns the response. */
export function httpExchange(options: HttpExchangeOptions = {}): Exchange {
  const { timeoutMs = 15_000, validate = (url: string) => validateWebhookUrl(url, options) } = options;
  return async ({ url, method, headers, body }) => {
    const signal = AbortSignal.timeout(timeoutMs);
    let target: PinnedTarget;
    try {
      target = await Promise.race([
        validate(url),
        new Promise<never>((_, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true })),
      ]);
    } catch {
      return null;
    }
    const send = target.url.protocol === "https:" ? httpsRequest : httpRequest;
    return new Promise<ExchangeResponse | null>((resolve) => {
      const req = send(
        target.url,
        {
          method,
          headers: { ...headers, ...(body === undefined ? {} : { "content-length": String(Buffer.byteLength(body)) }) },
          signal,
          lookup: (_host, lookupOptions, callback) => {
            if (lookupOptions.all) {
              (callback as unknown as (e: null, a: { address: string; family: number }[]) => void)(null, [{ address: target.address, family: target.family }]);
            } else {
              callback(null, target.address, target.family);
            }
          },
        },
        (res) => {
          const chunks: Buffer[] = [];
          let size = 0;
          const done = () => {
            const flat: Record<string, string | undefined> = {};
            for (const [k, v] of Object.entries(res.headers)) flat[k] = Array.isArray(v) ? v[0] : v;
            resolve({ status: res.statusCode ?? 0, headers: flat, body: Buffer.concat(chunks).toString("utf8") });
          };
          res.on("data", (c: Buffer) => {
            size += c.length;
            chunks.push(size > RESPONSE_CAP_BYTES ? c.subarray(0, c.length - (size - RESPONSE_CAP_BYTES)) : c);
            if (size >= RESPONSE_CAP_BYTES) {
              res.destroy();
              done();
            }
          });
          res.on("end", done);
          res.on("error", () => resolve(null));
        },
      );
      req.on("error", () => resolve(null));
      req.end(body);
    });
  };
}

// ---- classification ------------------------------------------------------------------------------------------

/** Retry-After as seconds: delta-seconds or an HTTP date. */
export function parseRetryAfter(value: string | undefined, nowMs: number): number | undefined {
  if (value === undefined) return undefined;
  const v = value.trim();
  if (/^\d{1,9}$/.test(v)) return Number(v);
  const at = Date.parse(v);
  return Number.isNaN(at) ? undefined : Math.max(0, Math.ceil((at - nowMs) / 1000));
}

export const EXCERPT_CHARS = 2000;

const NIL_LEGAL_ENTITY = "00000000-0000-0000-0000-000000000000";

/** Why an event without a legal entity is dead-lettered before the render (see NOT-IMPLEMENTED.md). */
export const NO_LEGAL_ENTITY =
  "render not attempted: the event carries no legalentityid extension (or the nil UUID), and a service token to the migration service names exactly one legal entity";

/** Truncates, strips control characters and removes every secret value. */
export function excerptOf(text: string, secrets: readonly string[]): string {
  let out = text;
  for (const secret of secrets) {
    if (secret.length >= 4) out = out.split(secret).join("***");
  }
  // eslint-disable-next-line no-control-regex
  out = out.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, " ");
  return out.length > EXCERPT_CHARS ? out.slice(0, EXCERPT_CHARS) + "…" : out;
}

/** 2xx delivers, 410 pauses, 429 and 5xx retry (honouring Retry-After), other 4xx follow the subscription's policy, 3xx retry. */
export function classifyStatus(status: number, onClientError: ClientErrorPolicy, retryAfterSeconds?: number): RestOutcome {
  if (status >= 200 && status <= 299) return { kind: "delivered", status };
  if (status === 410) return { kind: "pause", status };
  const retry: RestOutcome = { kind: "retry", status, ...(retryAfterSeconds === undefined ? {} : { retryAfterSeconds }) };
  if (status === 429 || status >= 500 || status < 400) return retry;
  return onClientError === "retry" ? retry : { kind: "dead", status };
}

// ---- the pusher ----------------------------------------------------------------------------------------------

/** A rest-push subscription opened for sending (credentials in clear; never log or return this). */
export interface RestTarget {
  tenantId: string;
  subscriptionId: string;
  urlTemplate: string;
  config: RestConfig;
  credentials: RestCredentials;
}

export interface RestPusherDeps {
  migration: MigrationClient;
  exchange: Exchange;
  /** Unix milliseconds. */
  nowMs?: () => number;
}

interface Prepared {
  url: string;
  headers: Record<string, string>;
  body: string;
  refs: unknown;
  lookups: LookupUse[];
  secrets: string[];
  oauthKey: string | null;
}

type Preparation = { ok: true; prepared: Prepared } | { ok: false; outcome: RestOutcome };

export class RestPusher {
  readonly #d: RestPusherDeps;
  readonly #tokens = new Map<string, { token: string; expiresAtMs: number }>();

  constructor(deps: RestPusherDeps) {
    this.#d = deps;
  }

  #now(): number {
    return (this.#d.nowMs ?? Date.now)();
  }

  #secrets(c: RestCredentials): string[] {
    return [c.password, c.token, c.value, c.clientSecret].filter((s): s is string => typeof s === "string" && s !== "");
  }

  /** Renders the event and builds the request, or the outcome that stops the attempt. */
  async prepare(target: RestTarget, event: HubEvent, forceFreshToken = false, actor?: string): Promise<Preparation> {
    const { config } = target;
    const secrets = this.#secrets(target.credentials);
    if (event.legalEntityId === undefined || event.legalEntityId === NIL_LEGAL_ENTITY) {
      // Service tokens name exactly one legal entity (platform/go/tenant VerifyContext refuses the nil one), so
      // without the event's legalentityid the migration service cannot be asked; nothing is pushed unmapped.
      // The nil UUID is caught here: the verifier would answer 401, which is retried as a key or clock fault.
      return { ok: false, outcome: { kind: "dead", status: null, excerpt: NO_LEGAL_ENTITY } };
    }
    const rendered = await this.#d.migration.render({
      tenantId: target.tenantId,
      legalEntityId: event.legalEntityId,
      mappingId: config.outboundMappingId,
      event: cloudEventOf(event),
      ...(actor === undefined ? {} : { actor }),
    });
    if (!rendered.ok) {
      const excerpt = excerptOf(`render ${rendered.status ?? "no answer"}${rendered.code ? ` ${rendered.code}` : ""}: ${rendered.detail}`, secrets);
      return { ok: false, outcome: rendered.retryable ? { kind: "retry", status: rendered.status, excerpt } : { kind: "dead", status: rendered.status, excerpt } };
    }
    let url: string;
    try {
      url = expandUrl(target.urlTemplate, rendered.rendered.body);
    } catch (err) {
      return { ok: false, outcome: { kind: "dead", status: null, excerpt: excerptOf(`url template: ${(err as Error).message}`, secrets) } };
    }
    const headers: Record<string, string> = {
      "content-type": "application/json",
      accept: "application/json",
      "idempotency-key": event.eventId,
    };
    let oauthKey: string | null = null;
    const { auth } = config;
    const c = target.credentials;
    switch (auth.type) {
      case "basic": {
        // The encoded pair is as good as the password: a receiver that echoes the header must not leak it.
        const pair = Buffer.from(`${auth.username}:${c.password ?? ""}`).toString("base64");
        headers["authorization"] = `Basic ${pair}`;
        secrets.push(pair);
        break;
      }
      case "bearer":
        headers["authorization"] = `Bearer ${c.token ?? ""}`;
        break;
      case "api-key":
        headers[(auth.headerName as string).toLowerCase()] = c.value ?? "";
        break;
      case "oauth2-client-credentials": {
        oauthKey = createHash("sha256").update(`${target.tenantId}\0${auth.tokenUrl}\0${auth.clientId}\0${auth.scope ?? ""}\0${c.clientSecret ?? ""}`).digest("hex");
        const token = await this.#token(oauthKey, target, forceFreshToken);
        if (token === null) {
          return { ok: false, outcome: { kind: "retry", status: null, excerpt: "oauth2 token request failed" } };
        }
        headers["authorization"] = `Bearer ${token}`;
        secrets.push(token);
        break;
      }
    }
    return { ok: true, prepared: { url, headers, body: JSON.stringify(rendered.rendered.body), refs: rendered.rendered.refs, lookups: rendered.rendered.lookups, secrets, oauthKey } };
  }

  async #token(key: string, target: RestTarget, fresh: boolean): Promise<string | null> {
    const cached = this.#tokens.get(key);
    if (!fresh && cached !== undefined && cached.expiresAtMs - 30_000 > this.#now()) return cached.token;
    const { auth } = target.config;
    const form = new URLSearchParams({ grant_type: "client_credentials", client_id: auth.clientId as string, client_secret: target.credentials.clientSecret ?? "" });
    if (auth.scope !== undefined) form.set("scope", auth.scope);
    const res = await this.#d.exchange({
      url: auth.tokenUrl as string,
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", accept: "application/json" },
      body: form.toString(),
    });
    if (res === null || res.status < 200 || res.status > 299) return null;
    try {
      const json = JSON.parse(res.body) as { access_token?: unknown; expires_in?: unknown };
      if (typeof json.access_token !== "string" || json.access_token === "") return null;
      const ttl = typeof json.expires_in === "number" && json.expires_in > 0 ? json.expires_in : 300;
      this.#tokens.set(key, { token: json.access_token, expiresAtMs: this.#now() + ttl * 1000 });
      return json.access_token;
    } catch {
      return null;
    }
  }

  async #send(target: RestTarget, event: HubEvent, p: Prepared, actor?: string): Promise<ExchangeResponse | null> {
    let res = await this.#d.exchange({ url: p.url, method: target.config.method, headers: p.headers, body: p.body });
    if (res !== null && res.status === 401 && p.oauthKey !== null) {
      // The cached token may have been revoked: one inline retry with a fresh one.
      const again = await this.prepare(target, event, true, actor);
      if (again.ok) {
        res = await this.#d.exchange({ url: again.prepared.url, method: target.config.method, headers: again.prepared.headers, body: again.prepared.body });
        p.secrets.push(...again.prepared.secrets);
      }
    }
    return res;
  }

  /** One delivery attempt for the Dispatcher: render, push, record the legacy key. */
  async attempt(target: RestTarget, delivery: PendingDelivery): Promise<RestOutcome> {
    const event = delivery.event;
    const prep = await this.prepare(target, event);
    if (!prep.ok) return prep.outcome;
    const out = await this.#push(target, event, prep.prepared);
    // The lookup table versions the render read stay with the push, whatever came of it.
    return prep.prepared.lookups.length === 0 ? out : { ...out, lookups: prep.prepared.lookups };
  }

  async #push(target: RestTarget, event: HubEvent, p: Prepared): Promise<RestOutcome> {
    const res = await this.#send(target, event, p);
    if (res === null) return { kind: "retry", status: null, excerpt: "no response from the receiver" };
    const excerpt = excerptOf(res.body, p.secrets);
    const outcome = classifyStatus(res.status, target.config.onClientError, parseRetryAfter(res.headers["retry-after"], this.#now()));
    if (outcome.kind !== "delivered") return { ...outcome, excerpt };
    let legacyKey: string | undefined;
    if (target.config.responseKeyPath !== "") {
      let json: unknown;
      try {
        json = JSON.parse(res.body);
      } catch {
        json = undefined;
      }
      const found = json === undefined ? undefined : readPath(json, target.config.responseKeyPath);
      if (typeof found === "number") legacyKey = String(found);
      else if (typeof found === "string" && found !== "") legacyKey = found;
      if (legacyKey === undefined) {
        // The receiver accepted the record but the hub cannot tell which one it created. Resending is safe
        // (same Idempotency-Key) but would not help; an operator fixes the path and replays.
        return { kind: "dead", status: res.status, excerpt: excerptOf(`accepted, but no legacy key at ${target.config.responseKeyPath}: ${res.body}`, p.secrets) };
      }
      let lineKeys: LineKey[] | undefined;
      let linesCollection: string | null | undefined;
      if ((target.config.lineKeyPath ?? "") !== "") {
        const m = lineKeysOf(target.config, JSON.parse(p.body) as unknown, json, p.refs);
        if (!m.ok) {
          // The receiver accepted the push but the line keys cannot be told apart: nothing is recorded (a key on the
          // wrong line is worse than none), and keys an earlier attempt read are cleared. Resending is safe (same
          // Idempotency-Key); an operator fixes the paths and replays.
          return { kind: "dead", status: res.status, excerpt: excerptOf(`accepted, but the line keys at ${target.config.lineKeyPath} cannot be matched: ${m.reason}`, p.secrets), legacyKey, lineKeys: [] };
        }
        lineKeys = m.keys;
        linesCollection = m.collection;
      }
      const xref = await this.#d.migration.recordXref({
        tenantId: target.tenantId,
        legalEntityId: event.legalEntityId ?? null,
        mappingId: target.config.outboundMappingId,
        refs: p.refs,
        legacyKey,
        eventId: event.eventId,
        // Sent only when the render named element refs; otherwise the keys are kept on the delivery alone.
        ...(lineKeys !== undefined && linesCollection !== undefined ? { lines: lineKeys, linesCollection } : {}),
      });
      if (!xref.ok) {
        const ex = excerptOf(`xref:record ${xref.status ?? "no answer"}${xref.code ? ` ${xref.code}` : ""}: ${xref.detail}`, p.secrets);
        if (xref.code === CODE_XREF_EVENT_CONFLICT) {
          // The receiver accepted the record, but this event already has another legacy key recorded for this
          // mapping, and the migration service never changes that. Resending cannot fix it and would create one more
          // record in the legacy ERP unless it honours Idempotency-Key: HUB-0118 keeps the key it returned for the
          // operator to reconcile there, and the store refuses to replay it.
          return { kind: "dead", code: "HUB-0118", status: xref.status, excerpt: excerptOf(`pushed (receiver ${res.status}); not resent. ${ex}`, p.secrets), legacyKey, ...(lineKeys === undefined ? {} : { lineKeys }) };
        }
        const kept = lineKeys === undefined ? {} : { lineKeys };
        return xref.retryable ? { kind: "retry", status: xref.status, excerpt: ex, legacyKey, ...kept } : { kind: "dead", status: xref.status, excerpt: ex, legacyKey, ...kept };
      }
      return { kind: "delivered", status: res.status, excerpt, legacyKey, ...(lineKeys === undefined ? {} : { lineKeys }) };
    }
    return { kind: "delivered", status: res.status, excerpt };
  }

  /** Test send: render and build the request; with `send`, push it too. Nothing is recorded as delivered or in the xref. */
  async preview(
    target: RestTarget,
    event: HubEvent,
    send: boolean,
  ): Promise<{ stage: "render" | "url" | "token" | "built" | "sent"; url?: string; method: string; body?: unknown; status?: number | null; excerpt?: string; legacyKey?: string; lineKeys?: LineKey[]; lineKeyError?: string; lookups?: LookupUse[] }> {
    // A test send is made for the operator who asked: their user id is the token's actor.
    const prep = await this.prepare(target, event, false, event.actor);
    if (!prep.ok) {
      const o = prep.outcome;
      return { stage: o.excerpt?.startsWith("url template") ? "url" : o.excerpt?.startsWith("oauth2") ? "token" : "render", method: target.config.method, status: o.status, ...(o.excerpt === undefined ? {} : { excerpt: o.excerpt }) };
    }
    const p = prep.prepared;
    const base = { url: p.url, method: target.config.method, body: JSON.parse(p.body) as unknown, ...(p.lookups.length === 0 ? {} : { lookups: p.lookups }) };
    if (!send) return { stage: "built", ...base };
    const res = await this.#send(target, event, p, event.actor);
    if (res === null) return { stage: "sent", ...base, status: null, excerpt: "no response from the receiver" };
    let legacyKey: string | undefined;
    let lineKeys: LineKey[] | undefined;
    let lineKeyError: string | undefined;
    if (target.config.responseKeyPath !== "") {
      try {
        const json = JSON.parse(res.body) as unknown;
        const found = readPath(json, target.config.responseKeyPath);
        if (typeof found === "string" || typeof found === "number") legacyKey = String(found);
        if ((target.config.lineKeyPath ?? "") !== "") {
          const m = lineKeysOf(target.config, base.body, json, p.refs);
          if (m.ok) lineKeys = m.keys;
          else lineKeyError = m.reason;
        }
      } catch {
        /* no JSON, no key */
      }
    }
    return {
      stage: "sent", ...base, status: res.status, excerpt: excerptOf(res.body, p.secrets),
      ...(legacyKey === undefined ? {} : { legacyKey }), ...(lineKeys === undefined ? {} : { lineKeys }), ...(lineKeyError === undefined ? {} : { lineKeyError }),
    };
  }
}
