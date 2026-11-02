import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BomVersionCreatePage, BomVersionPage, BomVersionsPage } from "./bom";
import { BomVersionEditPage } from "./bomEdit";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const bom = (id: string, extra: object = {}) => ({ id, productId: `PACK-${id}`, siteId: "S1", effectiveFrom: "2026-01-01", status: "draft", version: 2, ...extra });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const altLines = [
  { lineNo: 1, componentProductId: "CASE", quantity: "1", scrapConst: "0", scrapVarPct: "0" },
  { lineNo: 2, componentProductId: "CELL-A", quantity: "4", scrapConst: "0", scrapVarPct: "0", altGroup: "A1", altStrategy: "usage_probability", usageProbabilityPct: "60" },
  {
    lineNo: 3, componentProductId: "CELL-B", quantity: "4", scrapConst: "0", scrapVarPct: "0", altGroup: "A1", altStrategy: "usage_probability", usageProbabilityPct: "40",
    discontinueType: "single", discontinueOn: "2027-01-01", followUpComponentId: "CELL-C",
  },
];

describe("BOM usage and alternative groups", () => {
  it("filters the list by usage and shows it", async () => {
    const fetchMock = stub(() => ok({ items: [bom("a", { usage: "costing" })] }));
    render(<BomVersionsPage />);
    const table = await findTable({ name: "BOM versions" });
    expect(table).toHaveTextContent("costing");
    fireEvent.change(screen.getByLabelText(/Usage/), { target: { value: "costing" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions?usage=costing"));
  });

  it("shows usage, category, groups and discontinuation on the detail", async () => {
    stub(() => ok(bom("a", { usage: "costing", category: "document", lines: altLines })));
    render(<BomVersionPage id="a" />);
    const table = await findTable({ name: "BOM lines" });
    expect(table).toHaveTextContent("A1 60%");
    expect(table).toHaveTextContent("A1 40%");
    expect(table).toHaveTextContent("single on 2027-01-01, then CELL-C");
    expect(screen.getByText("costing")).toBeInTheDocument();
    expect(screen.getByText("document")).toBeInTheDocument();
  });

  it("posts usage, category and the alternative group of a line", async () => {
    const fetchMock = stub(() => ({ ok: true, status: 201, json: async () => bom("new") }));
    render(<BomVersionCreatePage />);
    fireEvent.change(screen.getByLabelText(/^Product/), { target: { value: "PACK" } });
    fireEvent.change(screen.getByLabelText(/Effective from/), { target: { value: "2026-02-01" } });
    fireEvent.change(screen.getByLabelText(/^Usage/), { target: { value: "costing" } });
    fireEvent.change(screen.getByLabelText(/Component 1/), { target: { value: "CELL-A" } });
    fireEvent.change(screen.getByLabelText(/Quantity 1/), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText(/Alternative group 1/), { target: { value: "A1" } });
    fireEvent.change(screen.getByLabelText(/Alternative strategy 1/), { target: { value: "usage_probability" } });
    fireEvent.change(screen.getByLabelText(/Probability % 1/), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Create BOM version" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const body = JSON.parse(fetchMock.mock.calls[0][1]?.body as string);
    expect(body).toMatchObject({ usage: "costing", category: "material" });
    expect(body.lines[0]).toMatchObject({ altGroup: "A1", altStrategy: "usage_probability", usageProbabilityPct: "100" });
    expect(body.lines[0].altPriority).toBeUndefined();
  });

  it("shows ENG-ALT-CONFLICT from the service", async () => {
    stub(() => ({ ok: false, status: 422, json: async () => ({ code: "ENG-ALT-CONFLICT", title: "Alternative group A1 is inconsistent: probabilities sum to 90, not 100." }) }));
    render(<BomVersionCreatePage />);
    fireEvent.change(screen.getByLabelText(/^Product/), { target: { value: "PACK" } });
    fireEvent.change(screen.getByLabelText(/Effective from/), { target: { value: "2026-02-01" } });
    fireEvent.change(screen.getByLabelText(/Component 1/), { target: { value: "CELL-A" } });
    fireEvent.change(screen.getByLabelText(/Quantity 1/), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Create BOM version" }));
    expect(await findAlert()).toHaveTextContent("probabilities sum to 90");
  });

  it("keeps usage, category and group data when a draft is edited and saved", async () => {
    const fetchMock = stub((_url, init) => (init?.method === "PUT" ? ok(bom("a", { version: 3 })) : ok(bom("a", { usage: "costing", category: "document", lines: altLines }))));
    const saved: string[] = [];
    render(<BomVersionEditPage id="a" onSaved={(id) => saved.push(id)} />);
    fireEvent.click(await screen.findByRole("button", { name: /Save/ }));
    await waitFor(() => expect(saved).toEqual(["a"]));
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")!;
    const body = JSON.parse(put[1]?.body as string);
    expect(body).toMatchObject({ usage: "costing", category: "document", version: 2 });
    expect(body.lines[2]).toMatchObject({ altGroup: "A1", usageProbabilityPct: "40", discontinueType: "single", followUpComponentId: "CELL-C" });
  });

  it("edits a line's alternative group and sends only the chosen strategy's value", async () => {
    const fetchMock = stub((_url, init) => (init?.method === "PUT" ? ok(bom("a", { version: 3 })) : ok(bom("a", { lines: altLines }))));
    render(<BomVersionEditPage id="a" />);
    fireEvent.change(await screen.findByLabelText(/Probability % 3/), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText(/Alternative strategy 2/), { target: { value: "priority" } });
    fireEvent.change(screen.getByLabelText(/^Priority 2/), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText(/^Usage/), { target: { value: "sales" } });
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true));
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")!;
    const body = JSON.parse(put[1]?.body as string);
    expect(body.usage).toBe("sales");
    expect(body.lines[1]).toMatchObject({ altGroup: "A1", altStrategy: "priority", altPriority: 1 });
    expect(body.lines[1].usageProbabilityPct).toBeUndefined();
    expect(body.lines[2]).toMatchObject({ altGroup: "A1", usageProbabilityPct: "30" });
    // Clearing the group drops every group field from the line.
    expect(body.lines[0].altGroup).toBeUndefined();
  });

  it("lists a usage changed by someone else in the conflict view and takes it with the saved header", async () => {
    let saved = 0;
    const fetchMock = stub((_url, init) => {
      if (init?.method === "PUT") return saved++ === 0 ? { ok: false, status: 409, json: async () => ({ code: "ENG-BOM-VERSION-CONFLICT", title: "stale" }) } : ok(bom("a", { version: 4 }));
      return ok(saved === 0 ? bom("a", { lines: altLines }) : bom("a", { version: 3, usage: "costing", lines: altLines }));
    });
    render(<BomVersionEditPage id="a" />);
    fireEvent.click(await screen.findByRole("button", { name: /Save/ }));
    expect(await screen.findByText(/usage: yours "production", saved "costing"/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Use saved header" }));
    fireEvent.click(screen.getByRole("button", { name: "Continue editing version 3" }));
    fireEvent.click(await screen.findByRole("button", { name: /Save/ }));
    await waitFor(() => expect(saved).toBe(2));
    const last = fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")[1];
    expect(JSON.parse(last[1]?.body as string)).toMatchObject({ usage: "costing", version: 3 });
  });
});
