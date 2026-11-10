import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BomVersionPage } from "./bom";
import { activateBomVersion, cancelBomVersion } from "./api/client";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const problem = (status: number, code: string, detail: string) => ({ ok: false, status, json: async () => ({ code, title: "Err", detail }) });
const bom = (status: string) => ({ id: "v1", productId: "BIKE", siteId: "S1", effectiveFrom: "2026-01-01", status, lines: [] });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("BOM lifecycle client", () => {
  it("posts activation and cancellation to the escaped version id", async () => {
    const fetchMock = stub((url) => ok(bom(url.endsWith(":cancel") ? "cancelled" : "active")));
    expect((await activateBomVersion("a/b")).status).toBe("active");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions/a%2Fb:activate", expect.objectContaining({ method: "POST" }));
    expect((await cancelBomVersion("a/b")).status).toBe("cancelled");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions/a%2Fb:cancel", expect.objectContaining({ method: "POST" }));
  });

  it("surfaces the code of a refused cancel", async () => {
    stub(() => problem(409, "ENG-BOM-IN-ECO", "an open ECO releases this draft"));
    expect(await cancelBomVersion("x").catch((e: unknown) => e)).toMatchObject({ code: "ENG-BOM-IN-ECO", message: "an open ECO releases this draft", status: 409 });
  });
});

describe("BomVersionPage lifecycle controls", () => {
  it("offers Activate and Cancel draft only for a draft", async () => {
    stub(() => ok(bom("active")));
    render(<BomVersionPage id="v1" />);
    await screen.findByText("BOM BIKE");
    expect(screen.queryByRole("button", { name: "Activate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel draft" })).toBeNull();
  });

  it("activates a draft and announces it", async () => {
    stub((_url, init) => (init?.method === "POST" ? ok(bom("active")) : ok(bom("draft"))));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Activate" })).toBeNull());
    expect(document.querySelector('[aria-live="polite"]')).toHaveTextContent("BOM BIKE is now active");
  });

  it("shows the service reason when activation needs an ECO", async () => {
    stub((_url, init) => (init?.method === "POST" ? problem(422, "ENG-BOM-ECO-REQUIRED", "release it through an ECO") : ok(bom("draft"))));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    expect(await findAlert()).toHaveTextContent("release it through an ECO");
    expect(screen.getByRole("button", { name: "Activate" })).toBeEnabled();
  });

  it("reports a 403 as a permission message", async () => {
    stub((_url, init) => (init?.method === "POST" ? problem(403, "AUTH-0003", "forbidden") : ok(bom("draft"))));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    expect(await findAlert()).toHaveTextContent("You do not have permission to change bills of material");
  });

  it("asks for confirmation before cancelling, then shows the version as cancelled", async () => {
    const fetchMock = stub((url, init) => (init?.method === "POST" && url.endsWith(":cancel") ? ok(bom("cancelled")) : ok(bom("draft"))));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel draft" }));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Keep draft" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel draft" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm cancel" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Activate" })).toBeNull());
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions/v1:cancel", expect.objectContaining({ method: "POST" }));
    expect(screen.getByText("Cancelled")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel draft" })).toBeNull();
    expect(document.querySelector('[aria-live="polite"]')).toHaveTextContent("BOM BIKE draft is cancelled");
  });

  it("keeps the draft and shows the reason when cancel is refused", async () => {
    stub((url, init) => (init?.method === "POST" && url.endsWith(":cancel") ? problem(409, "ENG-BOM-IN-ECO", "an open ECO releases this draft") : ok(bom("draft"))));
    render(<BomVersionPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel draft" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm cancel" }));
    expect(await findAlert()).toHaveTextContent("an open ECO releases this draft");
    expect(screen.getByRole("button", { name: "Activate" })).toBeEnabled();
  });
});
