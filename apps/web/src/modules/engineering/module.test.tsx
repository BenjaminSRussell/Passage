import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { PrincipalProvider } from "../../shell/principal";
import { loadModules } from "../registry";
import engineering from "./module";
import { EngineeringHome, WhereUsedPage } from "./pages";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(response: Partial<Response> & { json?: () => Promise<unknown> }) {
  const fetchMock = vi.fn().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("engineering module", () => {
  it("uses its folder name as id and declares engineering routes", () => {
    expect(engineering.id).toBe("engineering");
    expect(engineering.routes.length).toBeGreaterThan(0);
    expect(engineering.nav.every((n) => n.to.startsWith("/engineering") && n.duty === "engineering.read")).toBe(true);
  });

  it("declares the BOM routes and nav entry", () => {
    expect(engineering.routes.map((r) => r.path)).toEqual(
      expect.arrayContaining(["/engineering/boms", "/engineering/boms/new", "/engineering/boms/:id", "/engineering/boms/:id/edit"]),
    );
    expect(engineering.nav.find((n) => n.label === "Bills of material")).toMatchObject({ to: "/engineering/boms", duty: "engineering.read" });
  });

  it("declares the ECO routes and nav entry", () => {
    expect(engineering.routes.map((r) => r.path)).toEqual(
      expect.arrayContaining(["/engineering/ecos", "/engineering/ecos/new", "/engineering/ecos/:id"]),
    );
    expect(engineering.nav.find((n) => n.label === "Change orders")).toMatchObject({ to: "/engineering/ecos", duty: "engineering.read" });
  });

  it("declares the ECR routes and nav entry, with New gated on the BOM write duty", () => {
    expect(engineering.routes.map((r) => r.path)).toEqual(expect.arrayContaining(["/engineering/ecrs", "/engineering/ecrs/new", "/engineering/ecrs/:id"]));
    expect(engineering.routes.find((r) => r.path === "/engineering/ecrs/new")?.handle).toMatchObject({ duty: "engineering.bom.write" });
    expect(engineering.nav.find((n) => n.label === "Change requests")).toMatchObject({ to: "/engineering/ecrs", duty: "engineering.read" });
  });

  it("shows the ECR decisions only with engineering.ecr.review", async () => {
    const ecr = { id: "a", number: "ECR-1", title: "Swap", priority: "high", state: "under_review", products: ["BIKE"], requestedBy: "rita" };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ecr }));
    const detail = engineering.routes.find((r) => r.path === "/engineering/ecrs/:id")!.element;
    const reader = render(
      <PrincipalProvider duties={["engineering.read", "engineering.bom.write"]}>
        <MemoryRouter>{detail}</MemoryRouter>
      </PrincipalProvider>,
    );
    expect(await screen.findByRole("button", { name: "Withdraw" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    reader.unmount();
    render(
      <PrincipalProvider duties={["engineering.read", "engineering.ecr.review"]}>
        <MemoryRouter>{detail}</MemoryRouter>
      </PrincipalProvider>,
    );
    expect(await screen.findByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Withdraw" })).toBeNull();
  });

  it("declares the routing routes and nav entries", () => {
    expect(engineering.routes.map((r) => r.path)).toEqual(
      expect.arrayContaining(["/engineering/routes", "/engineering/routes/:id", "/engineering/routes/:id/edit"]),
    );
    expect(engineering.nav.map((n) => n.label)).toContain("Routings");
  });

  it("declares the resource routes and duty-gated nav entries", () => {
    expect(engineering.routes.map((r) => r.path)).toEqual(
      expect.arrayContaining([
        "/engineering/resources",
        "/engineering/resources/:id",
        "/engineering/resource-groups",
        "/engineering/capabilities",
        "/engineering/calendars",
        "/engineering/calendars/:id",
        "/engineering/calendars/:id/days",
        "/engineering/resource-groups/:id",
        "/engineering/capabilities/:id",
        "/engineering/resources/new",
        "/engineering/resource-groups/new",
        "/engineering/capabilities/new",
        "/engineering/calendars/new",
      ]),
    );
    expect(engineering.routes.filter((r) => r.path !== "/engineering" && r.path !== "/engineering/where-used" && r.path !== "/engineering/routes").every((r) => r.handle?.title)).toBe(true);
    const entries = engineering.nav.filter((n) => ["Resources", "Resource groups", "Capabilities", "Work calendars"].includes(n.label));
    expect(entries.map((n) => [n.label, n.to, n.duty])).toEqual([
      ["Resources", "/engineering/resources", "engineering.read"],
      ["Resource groups", "/engineering/resource-groups", "engineering.read"],
      ["Capabilities", "/engineering/capabilities", "engineering.read"],
      ["Work calendars", "/engineering/calendars", "engineering.read"],
    ]);
  });

  it("gates the set-up routes and their New buttons on engineering.resource.write", async () => {
    const created = ["/engineering/resources/new", "/engineering/resource-groups/new", "/engineering/capabilities/new", "/engineering/calendars/new"];
    expect(engineering.routes.filter((r) => created.includes(r.path ?? "")).map((r) => (r.handle as { duty?: string }).duty)).toEqual(created.map(() => "engineering.resource.write"));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ items: [] }) }));
    const calendars = engineering.routes.find((r) => r.path === "/engineering/calendars")!.element;
    const reader = render(
      <PrincipalProvider duties={["engineering.read"]}>
        <MemoryRouter>{calendars}</MemoryRouter>
      </PrincipalProvider>,
    );
    expect(await screen.findByText("No work calendars yet")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New calendar" })).toBeNull();
    reader.unmount();
    render(
      <PrincipalProvider duties={["engineering.read", "engineering.resource.write"]}>
        <MemoryRouter>{calendars}</MemoryRouter>
      </PrincipalProvider>,
    );
    expect(await screen.findByRole("button", { name: "New calendar" })).toBeInTheDocument();
  });

  it("shows Edit and Delete resource on the detail route only with engineering.resource.write", async () => {
    const mill = { id: "a", code: "M1", name: "Mill", type: "machine", siteId: "S1", exclusive: false, version: 1, capabilities: [] };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => mill }));
    const detail = engineering.routes.find((r) => r.path === "/engineering/resources/:id")!.element;
    const reader = render(
      <PrincipalProvider duties={["engineering.read"]}>
        <MemoryRouter>{detail}</MemoryRouter>
      </PrincipalProvider>,
    );
    expect(await screen.findByRole("heading", { name: /Resource/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit resource" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete resource" })).toBeNull();
    reader.unmount();
    render(
      <PrincipalProvider duties={["engineering.read", "engineering.resource.write"]}>
        <MemoryRouter>{detail}</MemoryRouter>
      </PrincipalProvider>,
    );
    expect(await screen.findByRole("button", { name: "Edit resource" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete resource" })).toBeInTheDocument();
  });

  it("offers to open resources, groups and capabilities from the overview", () => {
    const opened: string[] = [];
    render(
      <EngineeringHome
        onOpenResources={() => opened.push("resources")}
        onOpenResourceGroups={() => opened.push("groups")}
        onOpenCapabilities={() => opened.push("capabilities")}
      />,
    );
    for (const name of ["Open Resources", "Open Resource groups", "Open Capabilities"]) {
      fireEvent.click(screen.getByRole("button", { name }));
    }
    expect(opened).toEqual(["resources", "groups", "capabilities"]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("engineering");
  });

  it("shows the workspace heading", () => {
    render(<EngineeringHome />);
    expect(screen.getByRole("heading", { name: "Engineering data" })).toBeTruthy();
  });

  it("offers to open where-used from the overview", () => {
    const opened: string[] = [];
    render(<EngineeringHome onOpenWhereUsed={() => opened.push("where-used")} />);
    fireEvent.click(screen.getByRole("button", { name: "Open Where-used" }));
    expect(opened).toEqual(["where-used"]);
  });

  it("offers to open routings from the overview", () => {
    const opened: string[] = [];
    render(<EngineeringHome onOpenRoutes={() => opened.push("routes")} />);
    fireEvent.click(screen.getByRole("button", { name: "Open Routings" }));
    expect(opened).toEqual(["routes"]);
  });

  it("prefills the where-used component from initialComponent", () => {
    render(<WhereUsedPage initialComponent="SPOKE" />);
    expect(screen.getByLabelText("Component")).toHaveValue("SPOKE");
  });

  it("lists every parent level returned by the where-used report", async () => {
    const fetchMock = stubFetch({
      ok: true,
      json: async () => ({
        items: [{ productId: "WHEEL", versionId: "w1", level: 1, parents: [{ productId: "BIKE", versionId: "b1", level: 2 }] }],
      }),
    });
    render(<WhereUsedPage />);
    fireEvent.change(screen.getByLabelText("Component"), { target: { value: "SPOKE" } });
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2026-03-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Find parents" }));
    const table = await findTable({ name: "Where-used results" });
    expect(table).toHaveTextContent("WHEEL");
    expect(table).toHaveTextContent("BIKE");
    expect(screen.getByRole("status")).toHaveTextContent("2 parents found");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/boms:where-used?component=SPOKE&asOf=2026-03-01");
  });

  it("announces a failed lookup and shows no table", async () => {
    stubFetch({ ok: false });
    render(<WhereUsedPage />);
    fireEvent.change(screen.getByLabelText("Component"), { target: { value: "SPOKE" } });
    fireEvent.click(screen.getByRole("button", { name: "Find parents" }));
    expect(await findAlert()).toHaveTextContent("Where-used lookup failed");
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByLabelText("Component")).toHaveValue("SPOKE");
  });

  it("says so when nothing uses the component", async () => {
    stubFetch({ ok: true, json: async () => ({ items: [] }) });
    render(<WhereUsedPage />);
    fireEvent.change(screen.getByLabelText("Component"), { target: { value: "SPOKE" } });
    fireEvent.click(screen.getByRole("button", { name: "Find parents" }));
    expect(await findStatus()).toHaveTextContent("No active BOM uses this component on that date.");
    expect(screen.queryByRole("table")).toBeNull();
    expect(await screen.findByText("No parents found")).toBeInTheDocument();
  });
});
