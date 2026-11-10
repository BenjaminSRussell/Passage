import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CostComponent, CostGroup, StoredVersion } from "./api/client";
import { validateStructure } from "./CostStructurePages";
import productcost from "./module";
import { movesFor, validateVersion } from "./VersionsPage";

type Reply = { status: number; body: unknown };
type Handler = (method: string, url: URL, body: unknown) => Reply | undefined;
interface Call {
  method: string;
  url: URL;
  body: unknown;
  key: string | null;
}

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: Call[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const method = init.method ?? "GET";
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      const headers = (init.headers ?? {}) as Record<string, string>;
      calls.push({ method, url, body, key: headers["Idempotency-Key"] ?? null });
      const reply = handler(method, url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${method} ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {productcost.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = () => calls.filter((c) => c.method === "POST");

const dm: CostComponent = { id: "0190f0c2-0000-7000-8000-000000000001", code: "DM", name: "Direct material", costGroupType: "DirectMaterial", behaviour: "Variable", createdAt: "2026-10-27T10:00:00Z" };
const dl: CostComponent = { id: "0190f0c2-0000-7000-8000-000000000002", code: "DL", name: "Direct labor", costGroupType: "DirectManufacturing", behaviour: "Variable", createdAt: "2026-10-27T10:00:00Z" };
const le = "0190f0c2-0000-7000-8000-0000000000e1";
const std: StoredVersion = {
  id: "0190f0c2-0000-7000-8000-0000000000a1",
  legalEntityId: le,
  code: "STD-2027",
  type: "Standard",
  status: "Open",
  fallback: "Active",
  siteIds: [],
  version: 1,
  createdAt: "2026-10-27T10:00:00Z",
};

describe("slice 5 helpers", () => {
  it("offers only the moves the version state machine allows", () => {
    expect(movesFor({ type: "Standard", status: "Open" })).toEqual(["block", "activate"]);
    expect(movesFor({ type: "Planned", status: "Blocked" })).toEqual(["unblock", "activate"]);
    expect(movesFor({ type: "Simulation", status: "Open" })).toEqual(["block"]);
    expect(movesFor({ type: "Simulation", status: "Blocked" })).toEqual(["unblock"]);
    expect(movesFor({ type: "Standard", status: "Active" })).toEqual(["close"]);
    expect(movesFor({ type: "Standard", status: "Closed" })).toEqual([]);
  });

  it("checks codes the way the service does", () => {
    const ok = { code: "IND-MAT", name: "Material overhead", type: "Indirect" as const, behaviour: "Variable" as const };
    expect(validateStructure(ok)).toEqual({});
    expect(validateStructure({ ...ok, code: "-DM" }).code).toMatch(/upper-case/);
    expect(validateStructure({ ...ok, code: "A".repeat(21) }).code).toMatch(/1 to 20/);
    expect(validateStructure({ ...ok, name: "  " }).name).toMatch(/Enter a name/);
  });

  it("builds a version request and refuses bad ids, duplicate sites and a missing fallback", () => {
    const site = "0190f0c2-0000-7000-8000-0000000000b1";
    const base = { legalEntityId: ` ${le} `, code: " STD-2027 ", type: "Planned" as const, fallback: "Active" as const, fallbackVersionId: "", sites: "", effectiveFrom: "" };
    expect(validateVersion({ ...base, sites: `${site}, `, effectiveFrom: "2027-01-01" }).input).toEqual({
      legalEntityId: le,
      code: "STD-2027",
      type: "Planned",
      fallback: "Active",
      siteIds: [site],
      effectiveFrom: "2027-01-01T00:00:00Z",
    });
    expect(validateVersion({ ...base, legalEntityId: "LE-01" }).errors.legalEntityId).toMatch(/UUID/);
    expect(validateVersion({ ...base, sites: "PLANT-1" }).errors.sites).toMatch(/UUID/);
    expect(validateVersion({ ...base, sites: `${site},${site.toUpperCase()}` }).errors.sites).toMatch(/once/);
    expect(validateVersion({ ...base, fallback: "SpecifiedVersion" }).errors.fallbackVersionId).toMatch(/Choose/);
    expect(validateVersion({ ...base, fallback: "SpecifiedVersion", fallbackVersionId: std.id }).input?.fallbackVersionId).toBe(std.id);
  });
});

describe("cost components page", () => {
  it("shows the loading state, then the stored components and the count against the limit", async () => {
    let release: (r: Reply) => void = () => {};
    handler = () => undefined;
    vi.mocked(fetch).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = (r) => resolve(new Response(JSON.stringify(r.body), { status: r.status }));
        }),
    );
    const { container } = renderAt("/productcost/cost-components");
    expect(screen.getByRole("table", { name: "Cost components" })).toHaveAttribute("aria-busy", "true");
    release({ status: 200, body: { items: [dl, dm] } });
    const table = await findTable({ name: "Cost components" });
    await waitFor(() => expect(within(table).getByRole("row", { name: /DM/ })).toHaveTextContent("Direct material"));
    expect(within(table).getByRole("row", { name: /DL/ })).toHaveTextContent("Direct manufacturing");
    expect(screen.getByText("2 of 20")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and adds a component with an Idempotency-Key", async () => {
    let stored: CostComponent[] = [];
    handler = (method, url, body) => {
      if (url.pathname !== "/api/v1/product-costing/cost-components") return undefined;
      if (method === "GET") return { status: 200, body: { items: stored } };
      const created = { ...(body as CostComponent), id: dm.id, createdAt: dm.createdAt };
      stored = [created];
      return { status: 201, body: created };
    };
    const { container } = renderAt("/productcost/cost-components");
    expect(await screen.findByRole("heading", { name: "No cost components yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "dm" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " Direct material " } });
    fireEvent.click(screen.getByRole("button", { name: "Add component" }));
    expect(await findStatus()).toHaveTextContent("Component DM added");
    expect(posts()).toHaveLength(1);
    expect(posts()[0].body).toEqual({ code: "DM", name: "Direct material", costGroupType: "DirectMaterial", behaviour: "Variable" });
    expect(posts()[0].key).toMatch(/.{8,}/);
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Cost components" })).getByRole("row", { name: /DM/ })).toBeInTheDocument());
  });

  it("checks the form before calling the service", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/productcost/cost-components");
    await screen.findByRole("heading", { name: "No cost components yet" });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "D M" } });
    fireEvent.click(screen.getByRole("button", { name: "Add component" }));
    expect(screen.getByText(/Use 1 to 20 upper-case letters/)).toBeInTheDocument();
    expect(screen.getByText("Enter a name.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });

  it("shows a refusal and retries the same request with the same key", async () => {
    let attempts = 0;
    handler = (method) => {
      if (method === "GET") return { status: 200, body: { items: [dm] } };
      attempts++;
      return attempts === 1
        ? { status: 409, body: { code: "CST-0791", detail: "a cost component with code DL already exists" } }
        : { status: 201, body: dl };
    };
    renderAt("/productcost/cost-components");
    await screen.findByRole("row", { name: /DM/ });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "DL" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Direct labor" } });
    fireEvent.change(screen.getByLabelText("Cost group type"), { target: { value: "DirectManufacturing" } });
    fireEvent.click(screen.getByRole("button", { name: "Add component" }));
    expect(await findAlert()).toHaveTextContent("a cost component with code DL already exists");
    fireEvent.click(screen.getByRole("button", { name: "Add component" }));
    await findStatus();
    expect(posts()).toHaveLength(2);
    expect(posts()[1].key).toBe(posts()[0].key);
  });

  it("explains a failed load", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty productcost.view" } });
    renderAt("/productcost/cost-components");
    expect(await findAlert()).toHaveTextContent("Could not load cost components");
    expect(screen.getByRole("alert")).toHaveTextContent("missing duty productcost.view");
  });
});

describe("cost groups page", () => {
  it("maps a new group to a stored component", async () => {
    let groups: CostGroup[] = [];
    handler = (method, url, body) => {
      if (url.pathname === "/api/v1/product-costing/cost-components") return { status: 200, body: { items: [dm, dl] } };
      if (url.pathname !== "/api/v1/product-costing/cost-groups") return undefined;
      if (method === "GET") return { status: 200, body: { items: groups } };
      const g = { ...(body as CostGroup), id: "0190f0c2-0000-7000-8000-0000000000c1", componentCode: "DL", createdAt: dm.createdAt };
      groups = [g];
      return { status: 201, body: g };
    };
    const { container } = renderAt("/productcost/cost-groups");
    expect(await screen.findByRole("heading", { name: "No cost groups yet" })).toBeInTheDocument();
    await screen.findByRole("option", { name: "DL Direct labor" });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "MACH" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Machining" } });
    fireEvent.click(screen.getByRole("button", { name: "Add cost group" }));
    expect(screen.getByText("Choose the cost component the group maps to.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Cost component"), { target: { value: dl.id } });
    fireEvent.change(screen.getByLabelText("Behaviour"), { target: { value: "Fixed" } });
    fireEvent.click(screen.getByRole("button", { name: "Add cost group" }));
    const row = await within(screen.getByRole("table", { name: "Cost groups" })).findByRole("row", { name: /^MACH Machining/ });
    expect(row).toHaveTextContent("DL");
    expect(posts()[0].body).toEqual({ code: "MACH", name: "Machining", type: "DirectManufacturing", behaviour: "Fixed", componentId: dl.id });
    expect(posts()[0].key).not.toBeNull();
  });

  it("disables the form until a component exists", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/productcost/cost-groups");
    expect(await screen.findByText("Add a cost component first.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add cost group" })).toBeDisabled();
  });
});

describe("costing versions page", () => {
  it("lists stored versions, creates one and moves it under its version counter", async () => {
    let stored: StoredVersion[] = [];
    handler = (method, url, body) => {
      const p = url.pathname;
      if (p === "/api/v1/product-costing/versions" && method === "GET") return { status: 200, body: { items: stored } };
      if (p === "/api/v1/product-costing/versions") {
        const v = { ...std, ...(body as object) } as StoredVersion;
        stored = [v];
        return { status: 201, body: v };
      }
      const { version, action } = body as { version: number; action?: string };
      if (p === `/api/v1/product-costing/versions/${std.id}/transition` && action === "block") return { status: 200, body: { ...stored[0], status: "Blocked", version: version + 1 } };
      if (p === `/api/v1/product-costing/versions/${std.id}/activate`)
        return { status: 200, body: { ...stored[0], status: "Active", version: version + 1, activatedAt: "2026-10-28T08:00:00Z" } };
      return undefined;
    };
    const { container } = renderAt("/productcost/versions");
    expect(await screen.findByRole("heading", { name: "No costing versions yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: le } });
    fireEvent.change(screen.getByLabelText("Version code"), { target: { value: "STD-2027" } });
    fireEvent.click(screen.getByRole("button", { name: "Create version" }));
    expect(await findStatus()).toHaveTextContent("Version STD-2027 created, status Open.");
    const create = posts()[0];
    expect(create.url.pathname).toBe("/api/v1/product-costing/versions");
    expect(create.body).toEqual({ legalEntityId: le, code: "STD-2027", type: "Standard", fallback: "Active" });
    expect(create.key).not.toBeNull();

    const table = screen.getByRole("table", { name: "Costing versions" });
    fireEvent.click(await within(table).findByRole("button", { name: "Block STD-2027" }));
    await waitFor(() => expect(within(table).getByRole("row", { name: /STD-2027/ })).toHaveTextContent("Blocked"));
    expect(posts()[1].body).toEqual({ action: "block", version: 1 });

    fireEvent.click(screen.getByRole("button", { name: "Activate STD-2027" }));
    await waitFor(() => expect(within(table).getByRole("row", { name: /STD-2027/ })).toHaveTextContent("Active"));
    expect(posts()[2].url.pathname).toBe(`/api/v1/product-costing/versions/${std.id}/activate`);
    expect(posts()[2].body).toEqual({ version: 2 });
    expect(within(table).getByRole("row", { name: /STD-2027/ })).toHaveTextContent("2026-10-28");
    expect(screen.getByRole("button", { name: "Close STD-2027" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("reloads after a stale-version refusal so the next move uses the current counter", async () => {
    let reads = 0;
    handler = (method, url) => {
      if (method === "GET") {
        reads++;
        return { status: 200, body: { items: [reads === 1 ? std : { ...std, status: "Blocked", version: 2 }] } };
      }
      if (url.pathname.endsWith("/activate")) return { status: 409, body: { code: "CST-0714", detail: "current version is 2" } };
      return undefined;
    };
    renderAt("/productcost/versions");
    fireEvent.click(await screen.findByRole("button", { name: "Activate STD-2027" }));
    expect(await findAlert()).toHaveTextContent("current version is 2");
    await waitFor(() => expect(screen.getByRole("row", { name: /STD-2027/ })).toHaveTextContent("Blocked"));
    expect(reads).toBe(2);
  });

  it("offers a fallback version of the same legal entity only", async () => {
    const other = { ...std, id: "0190f0c2-0000-7000-8000-0000000000a2", code: "OTHER", legalEntityId: "0190f0c2-0000-7000-8000-0000000000e2" };
    handler = (method) => (method === "GET" ? { status: 200, body: { items: [std, other] } } : undefined);
    renderAt("/productcost/versions");
    await screen.findByRole("row", { name: /OTHER/ });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: le } });
    fireEvent.change(screen.getByLabelText("Fallback principle"), { target: { value: "SpecifiedVersion" } });
    const select = screen.getByLabelText("Fallback version");
    expect(within(select).getByRole("option", { name: "STD-2027 (Open)" })).toBeInTheDocument();
    expect(within(select).queryByRole("option", { name: /OTHER/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Version code"), { target: { value: "SIM-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create version" }));
    expect(screen.getByText("Choose the version to fall back to.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });

  it("filters by status and pages with the next cursor", async () => {
    const second = { ...std, id: "0190f0c2-0000-7000-8000-0000000000a3", code: "STD-2028" };
    handler = (method, url) => {
      if (method !== "GET") return undefined;
      if (url.searchParams.get("after") === std.id) return { status: 200, body: { items: [second] } };
      return { status: 200, body: { items: [std], next: std.id } };
    };
    renderAt("/productcost/versions");
    fireEvent.click(await screen.findByRole("button", { name: "Load more versions" }));
    expect(await screen.findByRole("row", { name: /STD-2028/ })).toBeInTheDocument();
    expect(screen.getByRole("row", { name: /STD-2027/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more versions" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "Active" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("Active"));
    expect(calls.at(-1)?.url.searchParams.get("after")).toBeNull();
  });

  it("explains a failed load", async () => {
    handler = () => ({ status: 503, body: { code: "CST-0799", detail: "the costing store is not configured" } });
    renderAt("/productcost/versions");
    expect(await findAlert()).toHaveTextContent("Could not load costing versions");
    expect(screen.getByRole("alert")).toHaveTextContent("CST-0799");
  });
});
