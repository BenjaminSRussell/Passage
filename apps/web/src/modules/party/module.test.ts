import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import { DUTY_READ, DUTY_WRITE } from "./format";
import party from "./module";

describe("party module", () => {
  it("uses its folder name as id and declares duty-gated routes under /party", () => {
    expect(party.id).toBe("party");
    expect(party.routes.map((r) => r.path)).toEqual([
      "/party",
      "/party/new",
      "/party/tax-registrations",
      "/party/duplicates",
      "/party/pending-changes",
      "/party/pending-changes/:id",
      "/party/vendor-prospects",
      "/party/vendor-prospects/:id",
      "/party/:id",
    ]);
    expect(party.routes.every((r) => r.element !== null && r.element !== undefined)).toBe(true);
    expect(party.nav).toEqual([
      { label: "Parties", to: "/party", duty: DUTY_READ },
      { label: "New party", to: "/party/new", duty: DUTY_WRITE },
      { label: "Tax registrations", to: "/party/tax-registrations", duty: DUTY_WRITE },
      { label: "Duplicate candidates", to: "/party/duplicates", duty: DUTY_READ },
      { label: "Credit limit approvals", to: "/party/pending-changes", duty: DUTY_READ },
      { label: "Supplier registrations", to: "/party/vendor-prospects", duty: DUTY_READ },
    ]);
  });

  it("never gates nav or routes on party.merge", () => {
    expect(party.nav.some((n) => n.duty === "party.merge" || /merge/i.test(n.label))).toBe(false);
    expect(
      party.routes.some((r) => {
        const duty = (r.handle as { duty?: string } | undefined)?.duty;
        return duty === "party.merge" || (typeof r.path === "string" && r.path.includes("merge"));
      }),
    ).toBe(false);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("party");
  });
});
