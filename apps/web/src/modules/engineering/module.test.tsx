import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, vi } from "vitest";
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

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("engineering");
  });

  it("shows the workspace heading", () => {
    render(<EngineeringHome />);
    expect(screen.getByRole("heading", { name: "Engineering data" })).toBeTruthy();
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
    const table = await screen.findByRole("table", { name: "Where-used results" });
    expect(table).toHaveTextContent("WHEEL");
    expect(table).toHaveTextContent("BIKE");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/boms:where-used?component=SPOKE&asOf=2026-03-01");
  });

  it("announces a failed lookup and shows no table", async () => {
    stubFetch({ ok: false });
    render(<WhereUsedPage />);
    fireEvent.change(screen.getByLabelText("Component"), { target: { value: "SPOKE" } });
    fireEvent.click(screen.getByRole("button", { name: "Find parents" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Where-used lookup failed");
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("says so when nothing uses the component", async () => {
    stubFetch({ ok: true, json: async () => ({ items: [] }) });
    render(<WhereUsedPage />);
    fireEvent.change(screen.getByLabelText("Component"), { target: { value: "SPOKE" } });
    fireEvent.click(screen.getByRole("button", { name: "Find parents" }));
    expect(await screen.findByText("No active BOM uses this component on that date.")).toBeTruthy();
  });
});
