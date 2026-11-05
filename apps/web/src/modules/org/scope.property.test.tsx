import { findTable } from "../../test-utils";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrgLayout } from "./pages";
import { OperatingUnitsPage, SitesPage, WarehousesPage } from "./StructurePages";
import { stubLocalStorage } from "./testStorage";

const SEED = 20261026;

function mulberry32(seed: number) {
  let state = seed;
  return () => {
    state = (state + 0x6d2b79f5) | 0;
    let t = Math.imul(state ^ (state >>> 15), 1 | state);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const rand = mulberry32(SEED);
const between = (min: number, max: number) => min + Math.floor(rand() * (max - min + 1));

const entities = Array.from({ length: 20 }, (_, i) => ({
  id: `le-${i}`,
  code: `LE${String(i).padStart(2, "0")}`,
  name: `Entity ${i}`,
  country: "US",
  currency: "USD",
  timeZone: "UTC",
  defaultLanguage: "en",
  status: "active",
  version: 1,
}));

const sites = entities.flatMap((e) =>
  Array.from({ length: between(1, 4) }, (_, k) => ({ id: `${e.id}-s${k}`, legalEntityId: e.id, code: `ST-${e.code}-${k}`, name: "Site", timeZone: "UTC", status: "active" })),
);
const units = entities.flatMap((e) =>
  Array.from({ length: between(0, 4) }, (_, k) => ({ id: `${e.id}-u${k}`, defaultLegalEntityId: e.id, code: `OU-${e.code}-${k}`, name: "Unit", type: "department", status: "active" })),
);
const warehouses = sites.flatMap((s) =>
  Array.from({ length: between(0, 2) }, (_, k) => ({ id: `${s.id}-w${k}`, legalEntityId: s.legalEntityId, siteId: s.id, code: `WH-${s.code}-${k}`, name: "Warehouse", type: "standard", status: "active" })),
);

const pages = [
  { name: "Operating units", path: "operating-units", element: <OperatingUnitsPage />, rows: units, owner: (r: (typeof units)[number]) => r.defaultLegalEntityId },
  { name: "Sites", path: "sites", element: <SitesPage />, rows: sites, owner: (r: (typeof sites)[number]) => r.legalEntityId },
  { name: "Warehouses", path: "warehouses", element: <WarehousesPage />, rows: warehouses, owner: (r: (typeof warehouses)[number]) => r.legalEntityId },
];

function respond(url: string, init?: RequestInit) {
  const pathname = new URL(url, "http://x").pathname;
  if (!pathname.startsWith("/api/v1/org/") || pathname.startsWith("/api/v1/org/v1/")) {
    return new Response("{}", { status: 404 });
  }
  const path = pathname.split("/").pop();
  const scoped = new Headers(init?.headers).get("X-Passage-LegalEntity");
  const source: Record<string, { rows: unknown[]; owner: (r: never) => string }> = {
    "legal-entities": { rows: entities, owner: (r: { id: string }) => r.id } as never,
    ...Object.fromEntries(pages.map((p) => [p.path, { rows: p.rows, owner: p.owner as never }])),
  };
  const { rows, owner } = source[path ?? ""];
  return new Response(JSON.stringify({ items: rows.filter((r) => !scoped || owner(r as never) === scoped) }));
}

function eulerWalk(size: number): number[] {
  const out = Array.from({ length: size }, (_, i) => Array.from({ length: size }, (_, j) => j).filter((j) => j !== i));
  const stack = [0];
  const walk: number[] = [];
  while (stack.length > 0) {
    const next = out[stack[stack.length - 1]].pop();
    if (next === undefined) {
      walk.push(stack.pop() as number);
    } else {
      stack.push(next);
    }
  }
  return walk.reverse();
}

function renderPage(element: React.ReactNode, path: string) {
  return render(
    <MemoryRouter initialEntries={[`/org/${path}`]}>
      <Routes>
        <Route element={<OrgLayout duties={["org.switcher.all-scope"]} />}>
          <Route path={`/org/${path}`} element={element} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const switcher = () => screen.getByLabelText("Legal entity") as HTMLSelectElement;
const bodyRows = (table: HTMLElement) =>
  within(table)
    .getAllByRole("row")
    .slice(1)
    .map((row) => (row as HTMLTableRowElement).cells[0].textContent ?? "");

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

describe(`legal-entity re-scoping (seed ${SEED})`, () => {
  it.each(pages)("$name page leaks no row across any ordered pair of entities and ALL", async (page) => {
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => respond(url, init)));
    renderPage(page.element, page.path);
    await waitFor(() => expect(switcher()).not.toBeDisabled());
    const targets = [...entities.map((e) => e.id), "ALL"];
    const table = await findTable({ name: page.name });
    const ownerOf = new Map(page.rows.map((r) => [r.code, page.owner(r as never)]));

    for (const index of eulerWalk(targets.length)) {
      const target = targets[index];
      if (switcher().value !== target) {
        fireEvent.change(switcher(), { target: { value: target } });
      }
      await waitFor(() => expect(table).not.toHaveAttribute("aria-busy"));
      const codes = bodyRows(table).filter((code) => ownerOf.has(code));
      const expected = page.rows.filter((r) => target === "ALL" || page.owner(r as never) === target).map((r) => r.code);
      expect(codes.sort()).toEqual(expected.sort());
      expect(codes.filter((code) => target !== "ALL" && ownerOf.get(code) !== target)).toEqual([]);
      expect(screen.queryByText("Viewing all entities") !== null).toBe(target === "ALL");
    }
  }, 120_000);

  it("never renders rows from a superseded scope that resolves late", async () => {
    const queue: { scope: string | null; release(): void }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) =>
        url.includes("/legal-entities")
          ? Promise.resolve(respond(url, init))
          : new Promise<Response>((resolve) =>
              queue.push({ scope: new Headers(init?.headers).get("X-Passage-LegalEntity"), release: () => resolve(respond(url, init)) }),
            ),
      ),
    );
    renderPage(<SitesPage />, "sites");
    await waitFor(() => expect(switcher()).not.toBeDisabled());
    await waitFor(() => expect(queue).toHaveLength(1));
    fireEvent.change(switcher(), { target: { value: "le-1" } });
    fireEvent.change(switcher(), { target: { value: "le-2" } });
    await waitFor(() => expect(queue.map((q) => q.scope)).toEqual(["le-0", "le-1", "le-2"]));

    for (const index of [1, 2, 0]) {
      await act(async () => queue[index].release());
    }
    const table = await findTable({ name: "Sites" });
    await waitFor(() => expect(table).not.toHaveAttribute("aria-busy"));
    const expected = sites.filter((s) => s.legalEntityId === "le-2").map((s) => s.code);
    expect(bodyRows(table).sort()).toEqual(expected.sort());
  });
});
