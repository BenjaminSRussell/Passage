import { demoNote, fakeCatalog, validateEntity } from "./metadata";
import { applyView, saveView, type Personalization } from "./personalization";

describe("metadata catalog and personalization", () => {
  it("exposes the wave-3 fake catalog", () => {
    const catalog = fakeCatalog();
    expect(catalog.get("platform.note")?.title).toBe("Note");
    expect(catalog.all().map((e) => e.id)).toContain("platform.note");
  });

  it("validates required and lookup fields", () => {
    expect(validateEntity(demoNote, {})).toEqual({ title: "Title is required" });
    expect(validateEntity(demoNote, { title: "A", amount: "x" }).amount).toMatch(/number/);
    expect(validateEntity(demoNote, { title: "A", status: "nope" }).status).toMatch(/known/);
    expect(validateEntity(demoNote, { title: "A", status: "open" })).toEqual({});
  });

  it("saves and applies a view", () => {
    const view = { id: "v1", name: "Mine", entity: "platform.note", columns: ["title"], density: "compact" as const };
    const next = applyView(saveView({ views: [] } satisfies Personalization, view), "v1");
    expect(next.activeViewId).toBe("v1");
    expect(next.views[0].density).toBe("compact");
  });
});
