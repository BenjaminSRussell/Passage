import type { PassageModule } from "../modules/registry";
import { AREAS, groupByArea } from "./areas";

const mod = (id: string): PassageModule => ({ id, title: id, routes: [], nav: [{ label: id, to: `/${id}` }] });

describe("groupByArea", () => {
  it("groups modules by area in AREAS order, leaving out empty areas", () => {
    const groups = groupByArea([mod("sales"), mod("ledger"), mod("production"), mod("tax")]);
    expect(groups.map((g) => g.area.id)).toEqual(["finance", "sales", "manufacturing"]);
    expect(groups[0].modules.map((m) => m.id)).toEqual(["ledger", "tax"]);
  });

  it("puts modules missing from AREAS under More, so none disappears", () => {
    const groups = groupByArea([mod("ledger"), mod("brand-new")]);
    expect(groups.at(-1)).toMatchObject({ area: { id: "more", title: "More" } });
    expect(groups.at(-1)?.modules.map((m) => m.id)).toEqual(["brand-new"]);
  });

  it("assigns every module folder to exactly one area", () => {
    const folders = Object.keys(import.meta.glob("../modules/*/module.ts")).map((p) => p.split("/")[2]);
    const assigned = AREAS.flatMap((a) => a.modules);
    expect(new Set(assigned).size).toBe(assigned.length);
    expect(folders.filter((f) => !assigned.includes(f))).toEqual([]);
  });
});
