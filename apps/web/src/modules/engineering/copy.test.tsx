import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BomVersionPage } from "./bom";
import { RouteVersionPage } from "./screens";
import { cancelRouteVersion, copyBomVersion, copyRouteVersion } from "./api/client";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
const problem = (status: number, code: string, detail: string) => ({ ok: false, status, json: async () => ({ code, title: "Err", detail }) });
const bom = { id: "v1", productId: "BIKE", siteId: "S1", effectiveFrom: "2026-01-01", status: "active", lines: [] };
const route = (status: string) => ({
  id: "r1", productId: "BIKE", siteId: "S1", routeCode: "R", versionNo: 2, effectiveFrom: "2026-01-01", status, operationCount: 0, operations: [],
});

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("copy and route cancel client", () => {
  it("posts the dates and an idempotency key to the escaped id", async () => {
    const fetchMock = stub(() => ok({ ...bom, id: "v2", status: "draft" }, 201));
    await copyBomVersion("a/b", { effectiveFrom: "2027-01-01" }, "key-1");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions/a%2Fb:copy", {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": "key-1" },
      body: JSON.stringify({ effectiveFrom: "2027-01-01" }),
    });
    await copyRouteVersion("a/b", { effectiveFrom: "2027-01-01", effectiveTo: "2027-12-31" }, "key-2");
    expect(fetchMock.mock.calls.at(-1)?.[0]).toBe("/api/v1/engineering/route-versions/a%2Fb:copy");
    await cancelRouteVersion("a/b");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/route-versions/a%2Fb:cancel", { method: "POST" });
  });
});

describe("BomVersionPage copy", () => {
  it("copies into a draft and opens it", async () => {
    const opened = vi.fn();
    const fetchMock = stub((url) => (url.endsWith(":copy") ? ok({ ...bom, id: "v2", status: "draft" }, 201) : ok(bom)));
    render(<BomVersionPage id="v1" onOpenCopy={opened} />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy to new version" }));
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    fireEvent.change(screen.getByLabelText("New effective from"), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    await waitFor(() => expect(opened).toHaveBeenCalledWith("v2"));
    const [, init] = fetchMock.mock.calls.at(-1)!;
    expect(JSON.parse(String(init?.body))).toEqual({ effectiveFrom: "2027-01-01" });
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    expect(screen.queryByLabelText("New effective from")).toBeNull();
  });

  it("refuses an end date before the start without calling the service", async () => {
    const fetchMock = stub(() => ok(bom));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy to new version" }));
    fireEvent.change(screen.getByLabelText("New effective from"), { target: { value: "2027-02-01" } });
    fireEvent.change(screen.getByLabelText("New effective to"), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await findAlert()).toHaveTextContent("Effective to must not be before effective from");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("shows the service reason and a permission message, keeping the form", async () => {
    let failure = problem(422, "ENG-BOM-CYCLE", "copy would close a cycle");
    stub((url) => (url.endsWith(":copy") ? failure : ok(bom)));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy to new version" }));
    fireEvent.change(screen.getByLabelText("New effective from"), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await findAlert()).toHaveTextContent("copy would close a cycle");
    failure = problem(403, "AUTH-0003", "forbidden");
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    await waitFor(() => expect(screen.getAllByRole("alert").map((a) => a.textContent).join()).toContain("You do not have permission to change bills of material"));
    fireEvent.click(screen.getByRole("button", { name: "Cancel copy" }));
    expect(screen.queryByLabelText("New effective from")).toBeNull();
  });
});

describe("copy to another product or site", () => {
  const twoLines = {
    ...bom,
    lines: [
      { lineNo: 20, componentProductId: "BELL", quantity: "1", scrapConst: "0", scrapVarPct: "0" },
      { lineNo: 10, componentProductId: "WHEEL", quantity: "2", scrapConst: "0", scrapVarPct: "0" },
    ],
  };

  it("sends only the changed product, site and the ticked lines", async () => {
    const fetchMock = stub((url) => (url.endsWith(":copy") ? ok({ ...twoLines, id: "v2", status: "draft" }, 201) : ok(twoLines)));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy to new version" }));
    expect(screen.getByLabelText("Product")).toHaveValue("BIKE");
    expect(screen.getByLabelText("Site")).toHaveValue("S1");
    fireEvent.change(screen.getByLabelText("New effective from"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "EBIKE" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "" } });
    fireEvent.click(screen.getByLabelText("Line 20"));
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const [, init] = fetchMock.mock.calls.at(-1)!;
    expect(JSON.parse(String(init?.body))).toEqual({ effectiveFrom: "2027-01-01", productId: "EBIKE", siteId: "", lineNos: [10] });
  });

  it("refuses a copy with no lines ticked without calling the service", async () => {
    const fetchMock = stub(() => ok(twoLines));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy to new version" }));
    fireEvent.change(screen.getByLabelText("New effective from"), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByLabelText("Line 10"));
    fireEvent.click(screen.getByLabelText("Line 20"));
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await findAlert()).toHaveTextContent("Choose at least one line to copy");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("shows the lineage of a copied version", async () => {
    stub(() => ok({ ...bom, copiedFromVersionId: "v0" }));
    render(<BomVersionPage id="v1" />);
    expect(await screen.findByText("Copied from")).toBeInTheDocument();
    expect(screen.getByText("v0")).toBeInTheDocument();
  });

  it("requires a site on a route copy", async () => {
    const fetchMock = stub(() => ok(route("active")));
    render(<RouteVersionPage id="r1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy to new version" }));
    fireEvent.change(screen.getByLabelText("New effective from"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: " " } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await findAlert()).toHaveTextContent("Site is required");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("group", { name: "Lines to copy" })).toBeNull();
  });
});

describe("RouteVersionPage copy and cancel", () => {
  it("copies a route version and opens the new draft", async () => {
    const opened = vi.fn();
    const fetchMock = stub((url) => (url.endsWith(":copy") ? ok({ ...route("draft"), id: "r2", versionNo: 3 }, 201) : ok(route("active"))));
    render(<RouteVersionPage id="r1" onOpenCopy={opened} />);
    fireEvent.click(await screen.findByRole("button", { name: "Copy to new version" }));
    fireEvent.change(screen.getByLabelText("New effective from"), { target: { value: "2027-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    await waitFor(() => expect(opened).toHaveBeenCalledWith("r2"));
    expect(fetchMock.mock.calls.at(-1)?.[0]).toBe("/api/v1/engineering/route-versions/r1:copy");
  });

  it("asks for confirmation before cancelling a draft, then shows it cancelled", async () => {
    const fetchMock = stub((url, init) => (init?.method === "POST" && url.endsWith(":cancel") ? ok(route("cancelled")) : ok(route("draft"))));
    render(<RouteVersionPage id="r1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel draft" }));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Keep draft" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel draft" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm cancel" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Activate" })).toBeNull());
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/route-versions/r1:cancel", { method: "POST" });
    expect(screen.getByText("Cancelled")).toBeInTheDocument();
    expect(document.querySelector('[aria-live="polite"]')).toHaveTextContent("Route R version 2 draft is cancelled");
  });

  it("offers no cancel for an active version and reports a refused cancel", async () => {
    stub(() => ok(route("active")));
    const view = render(<RouteVersionPage id="r1" />);
    await screen.findByText("Route R");
    expect(screen.queryByRole("button", { name: "Cancel draft" })).toBeNull();
    view.unmount();
    stub((url, init) => (init?.method === "POST" && url.endsWith(":cancel") ? problem(409, "ENG-ROUTE-STATE", "only a draft can be cancelled") : ok(route("draft"))));
    render(<RouteVersionPage id="r1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel draft" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm cancel" }));
    expect(await findAlert()).toHaveTextContent("only a draft can be cancelled");
  });
});
