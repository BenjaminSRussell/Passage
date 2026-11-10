import { demoNote, fakeCatalog, validateEntity } from "./metadata";

describe("metadata catalog", () => {
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
});
