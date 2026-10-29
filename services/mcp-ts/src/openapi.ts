import { isObject, stripTenantId, type ApprovalMode, type HttpMethod, type JsonObject, type ToolDefinition } from "./tool.js";

const VERBS = ["get", "post", "put", "patch", "delete"] as const;

/**
 * Generates tools from every OpenAPI operation that carries x-passage-tool.
 * Path and query parameters and the JSON request body become the input schema.
 * tenant_id is removed from it (FR-47.1): the tenant comes from the caller's token.
 * Header parameters are skipped; the gateway sets the headers itself.
 */
export function toolsFromOpenApi(doc: JsonObject): ToolDefinition[] {
  const paths = isObject(doc.paths) ? doc.paths : {};
  const out: ToolDefinition[] = [];
  for (const [path, item] of Object.entries(paths)) {
    if (!isObject(item)) continue;
    for (const verb of VERBS) {
      const op = item[verb];
      if (!isObject(op) || typeof op["x-passage-tool"] !== "string") continue;
      out.push(toolFromOperation(path, verb.toUpperCase() as HttpMethod, op));
    }
  }
  return out;
}

function toolFromOperation(path: string, method: HttpMethod, op: JsonObject): ToolDefinition {
  const name = String(op["x-passage-tool"]);
  const duty = op["x-passage-duty"];
  if (typeof duty !== "string") throw new Error(`${name}: x-passage-duty is required`);
  const approval = (typeof op["x-passage-approval"] === "string" ? op["x-passage-approval"] : "none") as ApprovalMode;

  const properties: JsonObject = {};
  const required: string[] = [];
  for (const p of Array.isArray(op.parameters) ? op.parameters : []) {
    if (!isObject(p) || typeof p.name !== "string" || p.in === "header" || p.in === "cookie") continue;
    properties[p.name] = isObject(p.schema) ? p.schema : { type: "string" };
    if (p.required === true || p.in === "path") required.push(p.name);
  }
  const content = isObject(op.requestBody) && isObject(op.requestBody.content) ? op.requestBody.content : {};
  const body = isObject(content["application/json"]) ? content["application/json"].schema : undefined;
  if (isObject(body) && isObject(body.properties)) Object.assign(properties, body.properties);
  if (isObject(body) && Array.isArray(body.required)) {
    required.push(...body.required.filter((r): r is string => typeof r === "string"));
  }

  const responses = isObject(op.responses) ? op.responses : {};
  const ok = isObject(responses["200"]) ? responses["200"] : {};
  const okContent = isObject(ok.content) ? ok.content["application/json"] : undefined;
  const outputSchema = isObject(okContent) && isObject(okContent.schema) ? okContent.schema : {};

  const readOnly = method === "GET";
  return {
    name,
    description: typeof op.summary === "string" ? op.summary : name,
    version: typeof op["x-passage-version"] === "number" ? op["x-passage-version"] : 1,
    method,
    path,
    sourceRef: typeof op.operationId === "string" ? op.operationId : `${method} ${path}`,
    inputSchema: stripTenantId({ type: "object", properties, required: [...new Set(required)] }),
    outputSchema,
    requiredDuty: duty,
    approvalMode: approval,
    readOnly,
    idempotent: method === "GET" || method === "PUT" || method === "DELETE",
  };
}
