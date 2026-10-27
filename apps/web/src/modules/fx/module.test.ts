import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import fx from "./module";

describe("fx module", () => {
  it("uses its folder name as id and declares its routes", () => {
    expect(fx.id).toBe("fx");
    expect(fx.routes.map((r) => r.path)).toEqual(["/fx/rates", "/fx/lookup", "/fx/convert"]);
    expect(fx.nav.every((n) => n.to.startsWith("/fx"))).toBe(true);
  });

  it("gates every entry on the duty the fx read routes enforce", () => {
    // services/fx/internal/api: GET /v1/fx/rates, /rates/history and POST /v1/fx/convert require "View exchange rates".
    expect(fx.nav.map((n) => n.duty)).toEqual(["View exchange rates", "View exchange rates", "View exchange rates"]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("fx");
  });
});
