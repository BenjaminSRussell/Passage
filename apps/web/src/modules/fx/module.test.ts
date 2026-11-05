import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import fx from "./module";

describe("fx module", () => {
  it("uses its folder name as id and declares its routes", () => {
    expect(fx.id).toBe("fx");
    expect(fx.routes.map((r) => r.path)).toEqual(["/fx/rates", "/fx/lookup", "/fx/convert", "/fx/rate-entry", "/fx/rate-types"]);
    expect(fx.nav.every((n) => n.to.startsWith("/fx"))).toBe(true);
  });

  it("gates every entry on the duty the fx read routes enforce", () => {
    // services/fx/internal/api: the read routes require "fx.rate.view"; POST /v1/fx/rates and PUT /v1/fx/rate-types/{code} require "fx.rate.maintain".
    expect(fx.nav.map((n) => n.duty)).toEqual(["fx.rate.view", "fx.rate.view", "fx.rate.view", "fx.rate.maintain", "fx.rate.maintain"]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("fx");
  });
});
