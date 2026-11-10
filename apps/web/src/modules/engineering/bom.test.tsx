import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BomVersionCreatePage, BomVersionPage, BomVersionsPage } from "./bom";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const problem = (status: number, code: string, title: string, extra: object = {}) => ({ ok: false, status, json: async () => ({ code, title, ...extra }) });
const BIKE = "11111111-1111-4111-8111-111111111111";
const WHEEL = "22222222-2222-4222-8222-222222222222";
const FRAME = "33333333-3333-4333-8333-333333333333";
const bom = (id: string, extra: object = {}) => ({ id, productId: `BIKE-${id}`, siteId: "S1", effectiveFrom: "2026-01-01", status: "active", ...extra });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("BomVersionsPage", () => {
  it("shows loading, rows and opens a row", async () => {
    stub(() => ok({ items: [bom("a"), bom("b", { status: "draft" })] }));
    const opened: string[] = [];
    render(<BomVersionsPage onOpen={(id) => opened.push(id)} />);
    expect(screen.getByRole("table", { name: "BOM versions" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "BOM versions" });
    await waitFor(() => expect(table).not.toHaveAttribute("aria-busy"));
    expect(table).toHaveTextContent("Draft");
    fireEvent.click(screen.getByText("BIKE-a"));
    expect(opened).toEqual(["a"]);
  });

  it("shows the empty state", async () => {
    stub(() => ok({ items: [] }));
    render(<BomVersionsPage />);
    expect(await screen.findByText("No BOM versions match")).toBeInTheDocument();
  });

  it("shows an error with retry", async () => {
    let calls = 0;
    stub(() => (calls++ === 0 ? problem(500, "X", "Server broke") : ok({ items: [bom("a")] })));
    render(<BomVersionsPage />);
    expect(await findAlert()).toHaveTextContent("Server broke");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("BIKE-a");
  });

  it("sends filters and pages with the cursor", async () => {
    const fetchMock = stub((url) => (url.includes("cursor=c1") ? ok({ items: [bom("b")] }) : ok({ items: [bom("a")], nextCursor: "c1" })));
    render(<BomVersionsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText("BIKE-b");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions?cursor=c1");
    fireEvent.change(screen.getByLabelText(/Status/), { target: { value: "draft" } });
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions?status=draft"));
    fireEvent.change(screen.getByLabelText(/Product/), { target: { value: BIKE } });
    fireEvent.change(screen.getByLabelText(/Effective on/), { target: { value: "2026-06-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenLastCalledWith(`/api/v1/engineering/bom-versions?productId=${BIKE}&status=draft&asOf=2026-06-01`),
    );
  });

  it("shows a blank site as all sites and the quantity range", async () => {
    stub(() => ok({ items: [bom("a", { siteId: "", fromQty: "10", toQty: "50" })] }));
    render(<BomVersionsPage />);
    const table = await findTable({ name: "BOM versions" });
    await waitFor(() => expect(table).toHaveTextContent("All sites"));
    expect(table).toHaveTextContent("10 to 50");
  });

  it("offers the create action", async () => {
    stub(() => ok({ items: [] }));
    const created = vi.fn();
    render(<BomVersionsPage onCreate={created} />);
    fireEvent.click(screen.getByRole("button", { name: "New BOM version" }));
    expect(created).toHaveBeenCalled();
  });
});

describe("BomVersionPage", () => {
  const detail = bom("a", {
    lines: [
      { lineNo: 2, componentProductId: "SPOKE", quantity: "36", scrapConst: "0", scrapVarPct: "2" },
      { lineNo: 1, componentProductId: "RIM", quantity: "2", scrapConst: "0", scrapVarPct: "0" },
    ],
  });

  it("shows the facts and lines in order and links to where-used", async () => {
    stub(() => ok(detail));
    const used: string[] = [];
    render(<BomVersionPage id="a" onWhereUsed={(c) => used.push(c)} />);
    const table = await findTable({ name: "BOM lines" });
    const rows = table.querySelectorAll("tbody tr");
    expect(rows[0]).toHaveTextContent("RIM");
    expect(rows[1]).toHaveTextContent("SPOKE");
    fireEvent.click(screen.getByRole("button", { name: "Where used SPOKE" }));
    expect(used).toEqual(["SPOKE"]);
    expect(screen.getByText("2026-01-01 to open")).toBeInTheDocument();
  });

  it("explains a missing version", async () => {
    stub(() => problem(404, "ENG-BOM-NOTFOUND", "BOM version not found"));
    render(<BomVersionPage id="zzz" />);
    expect(await findAlert()).toHaveTextContent("This BOM version does not exist");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("shows other errors with retry", async () => {
    stub(() => problem(500, "X", "Server broke"));
    render(<BomVersionPage id="a" />);
    expect(await findAlert()).toHaveTextContent("Server broke");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

describe("BomVersionCreatePage", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText(/^Product/), { target: { value: BIKE } });
    fireEvent.change(screen.getByLabelText(/Effective from/), { target: { value: "2026-02-01" } });
    fireEvent.change(screen.getByLabelText(/Component 1/), { target: { value: WHEEL } });
    fireEvent.change(screen.getByLabelText(/Quantity 1/), { target: { value: "2" } });
  }

  it("posts the BOM with an idempotency key and reports the new id", async () => {
    const fetchMock = stub(() => ({ ok: true, status: 201, json: async () => bom("new") }));
    const created: string[] = [];
    render(<BomVersionCreatePage onCreated={(id) => created.push(id)} />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    fireEvent.change(screen.getByLabelText(/Component 2/), { target: { value: FRAME } });
    fireEvent.change(screen.getByLabelText(/Quantity 2/), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create BOM version" }));
    await waitFor(() => expect(created).toEqual(["new"]));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/engineering/bom-versions");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    const body = JSON.parse(init?.body as string);
    expect(body).toMatchObject({ productId: BIKE, effectiveFrom: "2026-02-01", status: "draft" });
    expect(body.lines.map((l: { componentProductId: string; lineNo: number }) => [l.lineNo, l.componentProductId])).toEqual([[1, WHEEL], [2, FRAME]]);
  });

  it("removes a line and shows a permission error", async () => {
    stub(() => problem(403, "COM-0003", "Forbidden"));
    render(<BomVersionCreatePage />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    expect(screen.queryByLabelText(/Component 2/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Create BOM version" }));
    expect(await findAlert()).toHaveTextContent("You do not have permission to create BOM versions");
  });

  it("shows the service detail for a cycle, not the HTTP status text", async () => {
    stub(() => problem(422, "ENG-BOM-CYCLE", "Unprocessable Entity", { detail: "WHEEL is already an ancestor of BIKE" }));
    render(<BomVersionCreatePage />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Create BOM version" }));
    expect(await findAlert()).toHaveTextContent("WHEEL is already an ancestor of BIKE");
    expect(screen.queryByText("Unprocessable Entity")).toBeNull();
  });

  it("places validation messages on the header field and the failing line", async () => {
    stub(() =>
      problem(400, "COM-0001", "Bad Request", {
        detail: "request failed validation",
        errors: { effectiveTo: "must not be before effectiveFrom", "lines[1]": "quantity must be positive", lines: "must contain 1 to 100 lines" },
      }),
    );
    render(<BomVersionCreatePage />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    fireEvent.change(screen.getByLabelText(/Component 2/), { target: { value: FRAME } });
    fireEvent.change(screen.getByLabelText(/Quantity 2/), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Create BOM version" }));
    await waitFor(() => expect(screen.getByLabelText(/Component 2/)).toHaveAttribute("aria-invalid", "true"));
    expect(screen.getByLabelText(/Component 2/)).toHaveAccessibleDescription("quantity must be positive");
    expect(screen.getByLabelText(/Component 1/)).not.toHaveAttribute("aria-invalid");
    expect(screen.getByLabelText(/Effective to/)).toHaveAccessibleDescription("must not be before effectiveFrom");
    expect(screen.getByText("request failed validation: lines must contain 1 to 100 lines")).toBeInTheDocument();
  });

  it("reuses the idempotency key when the same BOM is retried and changes it when the BOM changes", async () => {
    let calls = 0;
    const fetchMock = stub(() => {
      calls += 1;
      if (calls === 1) throw new TypeError("Failed to fetch");
      return problem(422, "ENG-BOM-OVERLAP", "Unprocessable Entity", { detail: "overlaps an active version" });
    });
    render(<BomVersionCreatePage />);
    fill();
    const submit = screen.getByRole("button", { name: "Create BOM version" });
    fireEvent.click(submit);
    expect(await findAlert()).toHaveTextContent("Failed to fetch");
    fireEvent.click(submit);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    fireEvent.change(screen.getByLabelText(/Quantity 1/), { target: { value: "3" } });
    await screen.findByText("overlaps an active version");
    fireEvent.click(submit);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    const keys = fetchMock.mock.calls.map(([, init]) => new Headers(init?.headers).get("Idempotency-Key"));
    expect(keys[0]).toBeTruthy();
    expect(keys[1]).toBe(keys[0]);
    expect(keys[2]).not.toBe(keys[0]);
  });
});
