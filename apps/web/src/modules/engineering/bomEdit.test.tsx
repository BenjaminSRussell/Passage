import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BomVersionEditPage, diffLines } from "./bomEdit";
import { BomVersionPage } from "./bom";
import { updateBomVersion } from "./api/client";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) => ({ ok: false, status, json: async () => ({ code, title: "Err", detail, errors }) });
const line = (lineNo: number, component: string, quantity = "2") => ({ lineNo, componentProductId: component, quantity, scrapConst: "0", scrapVarPct: "5", series: "1", lineType: "item" });
const draft = (version = 3, lines = [line(1, "WHEEL"), line(2, "FRAME")], status = "draft") => ({
  id: "v1", productId: "BIKE", siteId: "S1", effectiveFrom: "2026-01-01", status, version, lines,
});

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const puts = (fetchMock: ReturnType<typeof stub>) => fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "PUT");

describe("updateBomVersion client", () => {
  it("puts the body to the escaped id with an idempotency key", async () => {
    const fetchMock = stub(() => ok(draft(4)));
    await updateBomVersion("a/b", { productId: "BIKE", effectiveFrom: "2026-01-01", lines: [], version: 3 }, "k1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/engineering/bom-versions/a%2Fb");
    expect(init.method).toBe("PUT");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("k1");
    expect(JSON.parse(init.body as string)).toMatchObject({ version: 3 });
  });
});

describe("diffLines", () => {
  it("compares decimals by value and fields the form does not show", () => {
    expect(diffLines([line(1, "WHEEL", "2.50")], [line(1, "WHEEL", "2.5")])).toEqual([]);
    expect(diffLines([line(1, "WHEEL")], [{ ...line(1, "WHEEL"), lineType: "phantom" }]).map((r) => r.lineNo)).toEqual([1]);
    expect(diffLines([line(1, "WHEEL")], [{ ...line(1, "WHEEL"), effectiveTo: "2026-12-31" }]).map((r) => r.lineNo)).toEqual([1]);
  });

  it("lists only the lines that differ, added, removed or changed", () => {
    const rows = diffLines([line(1, "WHEEL"), line(2, "FRAME", "9"), line(4, "SEAT")], [line(1, "WHEEL", "2.0"), line(2, "FRAME"), line(3, "HUB")]);
    expect(rows.map((r) => r.lineNo)).toEqual([2, 3, 4]);
    expect(rows[1].yours).toBeUndefined();
    expect(rows[2].server).toBeUndefined();
  });
});

describe("BomVersionEditPage", () => {
  it("shows a loading skeleton, then the loaded header and lines", async () => {
    stub(() => ok(draft()));
    render(<BomVersionEditPage id="v1" />);
    expect(screen.queryByLabelText("Component 1")).toBeNull();
    expect(await screen.findByLabelText("Component 1")).toHaveValue("WHEEL");
    expect(screen.getByLabelText("Component 2")).toHaveValue("FRAME");
    expect(screen.getByLabelText("Site")).toHaveValue("S1");
  });

  it("saves every line with the loaded version, keeping fields the form does not edit", async () => {
    const onSaved = vi.fn();
    const fetchMock = stub((_url, init) => (init?.method === "PUT" ? ok(draft(4)) : ok(draft())));
    render(<BomVersionEditPage id="v1" onSaved={onSaved} />);
    fireEvent.change(await screen.findByLabelText("Quantity 1"), { target: { value: "6" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    fireEvent.change(screen.getByLabelText("Component 2"), { target: { value: "HUB" } });
    fireEvent.change(screen.getByLabelText("Quantity 2"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("v1"));
    const body = JSON.parse(puts(fetchMock)[0][1]!.body as string);
    expect(body.version).toBe(3);
    expect(body.productId).toBe("BIKE");
    expect(body.lines).toEqual([
      { ...line(1, "WHEEL", "6") },
      { lineNo: 3, componentProductId: "HUB", quantity: "1", scrapConst: "0", scrapVarPct: "0" },
    ]);
  });

  it("shows field errors from the service", async () => {
    stub((_url, init) => (init?.method === "PUT" ? problem(400, "COM-0001", "request failed validation", { effectiveFrom: "is required as YYYY-MM-DD" }) : ok(draft())));
    render(<BomVersionEditPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    expect(await findAlert()).toHaveTextContent("request failed validation");
    expect(await screen.findByText("is required as YYYY-MM-DD")).toBeInTheDocument();
  });

  it("reports a 403 as a permission message", async () => {
    stub((_url, init) => (init?.method === "PUT" ? problem(403, "AUTH-0003", "forbidden") : ok(draft())));
    render(<BomVersionEditPage id="v1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    expect(await findAlert()).toHaveTextContent("You do not have permission to change bills of material");
  });

  it("explains a version conflict with the lines that differ and lets the editor keep their edit", async () => {
    let served = draft(3);
    const fetchMock = stub((_url, init) => {
      if (init?.method === "PUT") return puts(fetchMock).length === 1 ? problem(409, "ENG-BOM-VERSION-CONFLICT", "the draft is at version 4") : ok(draft(5));
      return ok(served);
    });
    render(<BomVersionEditPage id="v1" />);
    fireEvent.change(await screen.findByLabelText("Quantity 1"), { target: { value: "6" } });
    served = draft(4, [line(1, "WHEEL"), line(2, "FRAME", "3"), line(3, "HUB")]);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    const table = await screen.findByRole("table", { name: "Lines that differ" });
    expect(screen.getByText("Someone else saved this draft first")).toBeInTheDocument();
    expect(within(table).getByText("WHEEL x 6, scrap 5%")).toBeInTheDocument();
    expect(within(table).getByText("HUB x 2, scrap 5%")).toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    fireEvent.click(screen.getByRole("button", { name: "Keep my values and continue over version 4" }));
    expect(await screen.findByLabelText("Quantity 1")).toHaveValue(6);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(puts(fetchMock)).toHaveLength(2));
    expect(JSON.parse(puts(fetchMock)[1][1]!.body as string).version).toBe(4);
  });

  it("merges line by line: the editor takes saved lines and the saved header before saving", async () => {
    let served: ReturnType<typeof draft> & { toQty?: string } = draft(3);
    const fetchMock = stub((_url, init) => {
      if (init?.method === "PUT") return puts(fetchMock).length === 1 ? problem(409, "ENG-BOM-VERSION-CONFLICT", "stale") : ok(draft(5));
      return ok(served);
    });
    render(<BomVersionEditPage id="v1" />);
    fireEvent.change(await screen.findByLabelText("Quantity 1"), { target: { value: "6" } });
    served = { ...draft(4, [line(1, "WHEEL"), { ...line(2, "FRAME"), series: "10" }, line(3, "HUB")]), toQty: "50" };
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    const table = await screen.findByRole("table", { name: "Lines that differ" });
    // A field the form does not show (series) still appears, so keeping mine never overwrites it unseen.
    expect(within(table).getByText("FRAME x 2, scrap 5%, per 10")).toBeInTheDocument();
    expect(screen.getByText(/to quantity: yours "", saved "50"/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Use saved line 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Use saved line 3" }));
    fireEvent.click(screen.getByRole("button", { name: "Use saved header" }));
    expect(within(screen.getByRole("table", { name: "Lines that differ" })).getAllByRole("row")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Keep my values and continue over version 4" }));
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(puts(fetchMock)).toHaveLength(2));
    const body = JSON.parse(puts(fetchMock)[1][1]!.body as string);
    expect(body.version).toBe(4);
    expect(body.toQty).toBe("50");
    expect(body.lines).toEqual([{ ...line(1, "WHEEL", "6") }, { ...line(2, "FRAME"), series: "10" }, line(3, "HUB")]);
  });

  it("offers to drop a line the saved draft removed", async () => {
    let served = draft(3);
    stub((_url, init) => (init?.method === "PUT" ? problem(409, "ENG-BOM-VERSION-CONFLICT", "stale") : ok(served)));
    render(<BomVersionEditPage id="v1" />);
    fireEvent.change(await screen.findByLabelText("Quantity 1"), { target: { value: "6" } });
    served = draft(4, [line(1, "WHEEL", "6.000")]);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove line 2 as saved" }));
    expect(screen.queryByRole("table", { name: "Lines that differ" })).toBeNull();
    expect(screen.getByRole("button", { name: "Continue editing version 4" })).toBeInTheDocument();
  });

  it("lets the editor discard their edit and load the newer draft", async () => {
    let served = draft(3);
    stub((_url, init) => (init?.method === "PUT" ? problem(409, "ENG-BOM-VERSION-CONFLICT", "stale") : ok(served)));
    render(<BomVersionEditPage id="v1" />);
    fireEvent.change(await screen.findByLabelText("Component 1"), { target: { value: "RIM" } });
    served = draft(4, [line(1, "TYRE")]);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard my edits and load version 4" }));
    expect(await screen.findByLabelText("Component 1")).toHaveValue("TYRE");
    expect(screen.queryByLabelText("Component 2")).toBeNull();
  });

  it("refuses to edit a version that is not a draft", async () => {
    stub(() => ok(draft(3, [line(1, "WHEEL")], "active")));
    render(<BomVersionEditPage id="v1" onCancel={() => {}} />);
    expect(await findAlert()).toHaveTextContent("Only a draft can be edited; this version is active");
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
  });

  it("shows a not-found message and a retry for a failed load", async () => {
    stub(() => problem(404, "ENG-BOM-NOTFOUND", "BOM version not found"));
    const { unmount } = render(<BomVersionEditPage id="nope" />);
    expect(await findAlert()).toHaveTextContent("This BOM version does not exist");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    unmount();
    stub(() => problem(500, "COM-9999", "boom"));
    render(<BomVersionEditPage id="v1" />);
    expect(await findAlert()).toHaveTextContent("boom");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

describe("BomVersionPage edit control", () => {
  it("offers Edit draft only for a draft and only when the page can open it", async () => {
    const onEdit = vi.fn();
    stub(() => ok(draft()));
    const { unmount } = render(<BomVersionPage id="v1" onEdit={onEdit} />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit draft" }));
    expect(onEdit).toHaveBeenCalledWith("v1");
    expect(screen.getByText("Draft version")).toBeInTheDocument();
    unmount();
    stub(() => ok(draft(3, [], "active")));
    render(<BomVersionPage id="v1" onEdit={onEdit} />);
    await screen.findByText("BOM BIKE");
    expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  });
});
