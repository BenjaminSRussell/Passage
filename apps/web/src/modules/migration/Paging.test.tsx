import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import migration from "./module";

type Reply = { status: number; body: unknown };
let handler: (url: URL) => Reply;
let calls: URL[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const reply = handler(url);
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <PrincipalProvider duties={["migration.run", "migration.spec.write"]}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {migration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const run = (id: string, entity: string) => ({
  id,
  sourceSystem: "ax2012",
  mode: "rehearsal",
  entity,
  status: "succeeded",
  dryRun: false,
  mappingVersion: 1,
  counts: { extracted: 1, mapped: 1, created: 1, updated: 0, unchanged: 0, templated: 0, rejected: 0, warned: 0 },
  rejects: [],
  warnings: [],
  absent: [],
  createdAt: "2026-10-30T10:00:00Z",
});

describe("paged lists", () => {
  it("appends the next page of runs, keeps the filter and drops the button on the last page", async () => {
    handler = (url) =>
      url.searchParams.get("cursor") === "r1" ? json(200, { items: [run("0195f0a0-0000-7000-8000-0000000000a2", "vendor")] }) : json(200, { items: [run("0195f0a0-0000-7000-8000-0000000000a1", "customer")], nextCursor: "r1" });
    renderAt("/migration");
    const table = await findTable({ name: "Migration runs" });
    expect(table).toHaveTextContent("customer");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByRole("table")).toHaveTextContent("vendor"));
    expect(screen.getByRole("table")).toHaveTextContent("customer");
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("pages mapping specs and data templates", async () => {
    const spec = (id: string, version: number) => ({ id, entity: "customer", version, checksum: "ab".repeat(32), fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z" });
    handler = (url) =>
      url.searchParams.get("cursor") ? json(200, { items: [spec("0195f0a0-0000-7000-8000-0000000000b2", 1)] }) : json(200, { items: [spec("0195f0a0-0000-7000-8000-0000000000b1", 2)], nextCursor: "s1" });
    const { unmount } = renderAt("/migration/specs");
    await findTable({ name: "Mapping specs" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getAllByRole("row")).toHaveLength(3));
    expect(calls.some((c) => c.pathname.endsWith("/mapping-specs") && c.searchParams.get("cursor") === "s1")).toBe(true);
    unmount();

    const tmpl = (id: string, version: number) => ({ id, entity: "customer", name: "Retail", version, checksum: "cd".repeat(32), defaultCount: 1, createdAt: "2026-10-30T09:00:00Z" });
    handler = (url) =>
      url.searchParams.get("cursor") ? json(200, { items: [tmpl("0195f0a0-0000-7000-8000-0000000000c2", 1)] }) : json(200, { items: [tmpl("0195f0a0-0000-7000-8000-0000000000c1", 2)], nextCursor: "t1" });
    renderAt("/migration/templates");
    await findTable({ name: "Data templates" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getAllByRole("row")).toHaveLength(3));
  });

  it("keeps the loaded rows and shows an alert when the next page fails, then recovers", async () => {
    let fail = true;
    handler = (url) => {
      if (!url.searchParams.get("cursor")) return json(200, { items: [run("0195f0a0-0000-7000-8000-0000000000a1", "customer")], nextCursor: "r1" });
      if (fail) return json(500, { code: "COM-0500", detail: "boom" });
      return json(200, { items: [run("0195f0a0-0000-7000-8000-0000000000a2", "vendor")] });
    };
    renderAt("/migration");
    await findTable({ name: "Migration runs" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Could not load more runs")).toBeInTheDocument();
    expect(screen.getByRole("table")).toHaveTextContent("customer");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByRole("table")).toHaveTextContent("vendor"));
    expect(screen.queryByText("Could not load more runs")).not.toBeInTheDocument();
  });

  it("pages the lineage of a templated run", async () => {
    const id = "0195f0a0-0000-7000-8000-0000000000a1";
    const row = (key: string) => ({ sourceKey: key, targetId: "0195f0a0-0000-7000-8000-0000000000e1", action: "created", defaultFromTemplate: ["segment"] });
    handler = (url) => {
      if (url.pathname.endsWith("/ledger")) return url.searchParams.get("cursor") ? json(200, { items: [row("C2")] }) : json(200, { items: [row("C1")], nextCursor: "l1" });
      return json(200, { ...run(id, "customer"), dataTemplateId: "0195f0a0-0000-7000-8000-0000000000c1" });
    };
    renderAt(`/migration/runs/${id}`);
    const lineage = await findTable({ name: "Rows that took template defaults" });
    expect(lineage).toHaveTextContent("C1");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Rows that took template defaults" })).toHaveTextContent("C2"));
  });
  it("tells the start-run pickers' user when more exist, offers a linked spec past the first page and asks for its entity's templates", async () => {
    const linked = "0195f0a0-0000-7000-8000-0000000000b9";
    handler = (url) => {
      if (url.pathname.endsWith(`/mapping-specs/${linked}`)) return json(200, { id: linked, entity: "vendor", version: 7, checksum: "ab".repeat(32), fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z", mapping: {} });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [{ id: "0195f0a0-0000-7000-8000-0000000000b1", entity: "customer", version: 2, checksum: "ab".repeat(32), fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z" }], nextCursor: "s1" });
      if (url.pathname.endsWith("/data-templates")) return json(200, { items: [{ id: "0195f0a0-0000-7000-8000-0000000000c1", entity: "vendor", name: "Retail", version: 1, checksum: "cd".repeat(32), defaultCount: 1, createdAt: "2026-10-30T09:00:00Z" }], nextCursor: "t1" });
      if (url.pathname.endsWith("/projects")) return json(200, { items: [], nextCursor: "p1" });
      return json(404, { code: "TEST-404", detail: "no fake" });
    };
    renderAt(`/migration/runs/new?spec=${linked}`);
    const spec = await screen.findByRole("combobox", { name: /Mapping spec/ });
    await waitFor(() => expect(spec).toHaveValue(linked));
    expect(screen.getByRole("option", { name: "vendor v7" })).toBeInTheDocument();
    expect(screen.getByText(/Only the first 200 specs are listed/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/Only the first 200 templates for vendor are listed/)).toBeInTheDocument());
    expect(screen.getByRole("option", { name: "Retail v1 (vendor)" })).toBeInTheDocument();
    expect(screen.getByText(/Only the first 200 active projects are listed/)).toBeInTheDocument();
    const templateCall = calls.filter((c) => c.pathname.endsWith("/data-templates")).at(-1);
    expect(templateCall?.searchParams.get("entity")).toBe("vendor");
    expect(templateCall?.searchParams.get("limit")).toBe("200");
    expect(calls.find((c) => c.pathname.endsWith("/mapping-specs"))?.searchParams.get("limit")).toBe("200");
  });
});
