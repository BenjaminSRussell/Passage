import { describe, expect, it } from "vitest";
import { loadConfig } from "../src/main.js";

const base = { PASSAGE_API_URL: "http://api.passage.svc:8080", PASSAGE_OPENAPI_JSON: "/etc/passage/openapi.json" };

describe("loadConfig (12-factor startup config)", () => {
  it("applies the defaults for address and build", () => {
    expect(loadConfig(base)).toEqual({
      apiUrl: base.PASSAGE_API_URL,
      openApiFile: base.PASSAGE_OPENAPI_JSON,
      host: "0.0.0.0",
      port: 8080,
      build: "0.1.0",
    });
  });

  it("reads HTTP_ADDR and SERVICE_VERSION", () => {
    expect(loadConfig({ ...base, HTTP_ADDR: "127.0.0.1:9000", SERVICE_VERSION: "abc123" })).toMatchObject({
      host: "127.0.0.1",
      port: 9000,
      build: "abc123",
    });
  });

  it.each([
    ["a non-http API URL", { ...base, PASSAGE_API_URL: "ftp://x" }, /PASSAGE_API_URL/],
    ["a missing OpenAPI file", { PASSAGE_API_URL: base.PASSAGE_API_URL }, /PASSAGE_OPENAPI_JSON/],
    ["an address without a port", { ...base, HTTP_ADDR: "localhost" }, /HTTP_ADDR/],
    ["a port out of range", { ...base, HTTP_ADDR: ":70000" }, /HTTP_ADDR/],
  ])("refuses %s at startup", (_name, env, message) => {
    expect(() => loadConfig(env)).toThrow(message);
  });
});
