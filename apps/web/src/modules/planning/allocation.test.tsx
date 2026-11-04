import { findTable } from "../../test-utils";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AllocationKey } from "./api/client";
import { allocateOutcome, draftOfKey, emptyKeyDraft, monthEnd, shareTotal, validateAllocate, validateKey } from "./AllocationPages";
import planning from "./module";

const ITEM_A = "00000000-0000-0000-0000-0000000000a1";
const ITEM_B = "00000000-0000-0000-0000-0000000000b2";
const SITE = "00000000-0000-0000-0000-0000000000c3";

const famA: AllocationKey = {
  id: "00000000-0000-0000-0000-0000000000d1",
  version: 1,
  code: "FAM-A",
  name: "Family A",
  forecastUom: "CASE",
  lineCount: 2,
  lines: [
    { itemId: ITEM_A, share: "60.000000", uomFactor: "1.000000000" },
    { itemId: ITEM_B, share: "40.000000", uomFactor: "6.000000000" },
  ],
};
const famAList: AllocationKey = { ...famA, lines: [] };

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = [match: string, respond: (url: string, init?: RequestInit) => Response];

function stubApi(...routes: Handler[]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("validateKey", () => {
  it("accepts a key whose shares add up to 100 and returns the body trimmed", () => {
    const draft = { ...draftOfKey(famA), code: "fam-a" };
    const { errors, body } = validateKey(draft, true);
    expect(errors).toEqual({});
    expect(body).toEqual({ name: "Family A", forecastUom: "CASE", lines: famA.lines });
  });

  it("adds shares exactly, to six decimals", () => {
    const lines = [
      { itemId: ITEM_A, share: "33.333333", uomFactor: "1" },
      { itemId: ITEM_B, share: "66.666667", uomFactor: "1" },
    ];
    expect(shareTotal(lines)).toBe("100");
    expect(shareTotal([{ itemId: ITEM_A, share: "0.1", uomFactor: "1" }, { itemId: ITEM_B, share: "0.2", uomFactor: "1" }])).toBe("0.3");
  });

  it("names every rule the service enforces", () => {
    const draft = {
      code: "x",
      name: "",
      forecastUom: "much too long",
      lines: [
        { itemId: "nope", share: "0", uomFactor: "1" },
        { itemId: ITEM_B, share: "50", uomFactor: "0" },
        { itemId: ITEM_B, share: "1.1234567", uomFactor: "1" },
      ],
    };
    const { errors, body } = validateKey(draft, true);
    expect(body).toBeNull();
    expect(Object.keys(errors).sort()).toEqual(["code", "forecastUom", "lines.0.itemId", "lines.0.share", "lines.1.uomFactor", "lines.2.itemId", "lines.2.share", "name"]);
  });

  it("refuses shares that do not add up to 100 and an empty key", () => {
    const draft = draftOfKey({ ...famA, lines: [{ itemId: ITEM_A, share: "99.999999", uomFactor: "1" }] });
    expect(validateKey(draft, false).errors.lines).toBe("The shares add up to 99.999999; they must add up to 100.");
    expect(validateKey({ ...draft, lines: [] }, false).errors.lines).toMatch(/Add 1 to 1000 items/);
  });

  it("does not check the code of an existing key", () => {
    expect(validateKey(draftOfKey(famA), false).errors).toEqual({});
    expect(validateKey(emptyKeyDraft(), true).errors.code).toBeTruthy();
  });
});

describe("validateAllocate", () => {
  it("builds one period per month with its last day", () => {
    const { errors, body } = validateAllocate({ modelCode: "main", siteId: SITE, firstMonth: "2027-01-01", quantities: "100, 10.5\n0" }, "FAM-A");
    expect(errors).toEqual({});
    expect(body).toEqual({
      keyCode: "FAM-A",
      siteId: SITE,
      periods: [
        { periodStart: "2027-01-01", periodEnd: "2027-01-31", qty: "100" },
        { periodStart: "2027-02-01", periodEnd: "2027-02-28", qty: "10.5" },
        { periodStart: "2027-03-01", periodEnd: "2027-03-31", qty: "0" },
      ],
    });
    expect(monthEnd("2028-02-01")).toBe("2028-02-29");
  });

  it("rolls past the end of the year", () => {
    const { body } = validateAllocate({ modelCode: "MAIN", siteId: SITE, firstMonth: "2026-12-01", quantities: "1 2" }, "FAM-A");
    expect(body?.periods.map((p) => p.periodStart)).toEqual(["2026-12-01", "2027-01-01"]);
  });

  it("names every rule", () => {
    const { errors, body } = validateAllocate({ modelCode: "x", siteId: "nope", firstMonth: "2027-01-15", quantities: "-1" }, "FAM-A");
    expect(body).toBeNull();
    expect(Object.keys(errors).sort()).toEqual(["firstMonth", "modelCode", "quantities", "siteId"]);
  });

  it("states the outcome", () => {
    const model = { code: "MAIN", version: 4, reductionPrinciple: "transactions" as const, reductionPercent: "0", status: "draft" as const };
    expect(allocateOutcome("MAIN", { model, keyCode: "FAM-A", keyVersion: 1, items: 2, periods: 3, cellsWritten: 6 })).toBe(
      "Forecast model MAIN: 6 manual cells written for 2 items over 3 months. The model is at version 4.",
    );
  });
});

describe("Allocation keys page", () => {
  it("lists the keys and opens one", async () => {
    stubApi(["/forecast-allocation-keys/FAM-A", () => json(famA)], ["/forecast-allocation-keys", () => json({ items: [famAList] })]);
    renderAt("/planning/allocation-keys");
    expect(screen.getByRole("heading", { level: 2, name: "Allocation keys" })).toBeInTheDocument();
    const table = await findTable({ name: "Allocation keys" });
    await waitFor(() => expect(within(table).getByText("FAM-A")).toBeInTheDocument());
    expect(within(table).getByText("CASE")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "FAM-A" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/planning/allocation-keys/FAM-A");
    expect(await screen.findByLabelText("Name")).toHaveValue("Family A");
  });

  it("shows the empty state", async () => {
    stubApi(["/forecast-allocation-keys", () => json({ items: [] })]);
    renderAt("/planning/allocation-keys");
    expect(await screen.findByText("No allocation keys yet")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/forecast-allocation-keys", () => problem(403, "AUTH-0003", "missing duty planning.read")]);
    renderAt("/planning/allocation-keys");
    expect(await screen.findByText("Could not load allocation keys")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("New allocation key page", () => {
  it("validates before sending, then creates with an Idempotency-Key and opens the key", async () => {
    const fetchMock = stubApi(["/forecast-allocation-keys/FAM-A", () => json(famA)], ["/forecast-allocation-keys", () => json(famA, 201)]);
    renderAt("/planning/allocation-keys/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "fam-a" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Family A" } });
    fireEvent.change(screen.getByLabelText("Forecast unit"), { target: { value: "CASE" } });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM_A } });
    fireEvent.change(screen.getByLabelText("Share (%)"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Create allocation key" }));
    expect(await screen.findByText("The shares add up to 60; they must add up to 100.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Add item" }));
    const items = screen.getAllByLabelText("Item");
    fireEvent.change(items[1], { target: { value: ITEM_B } });
    fireEvent.change(screen.getAllByLabelText("Share (%)")[1], { target: { value: "40" } });
    fireEvent.change(screen.getAllByLabelText("Inventory units per forecast unit")[1], { target: { value: "6" } });
    expect(screen.getByText("Shares add up to 100%.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create allocation key" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/planning/allocation-keys/FAM-A"));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/planning/forecast-allocation-keys");
    expect(init?.method).toBe("POST");
    expect((init?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(init?.body))).toEqual({
      code: "FAM-A",
      name: "Family A",
      forecastUom: "CASE",
      lines: [
        { itemId: ITEM_A, share: "60", uomFactor: "1" },
        { itemId: ITEM_B, share: "40", uomFactor: "6" },
      ],
    });
  });

  it("removes an item and shows a taken code", async () => {
    stubApi(["/forecast-allocation-keys", () => problem(409, "PLAN-ALLOCATION-KEY-EXISTS", "an allocation key with code FAM-A already exists")]);
    renderAt("/planning/allocation-keys/new");
    fireEvent.click(screen.getByRole("button", { name: "Add item" }));
    expect(screen.getAllByLabelText("Item")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Remove item 2" }));
    expect(screen.getAllByLabelText("Item")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Remove item 1" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "FAM-A" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Family A" } });
    fireEvent.change(screen.getByLabelText("Forecast unit"), { target: { value: "CASE" } });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM_A } });
    fireEvent.change(screen.getByLabelText("Share (%)"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Create allocation key" }));
    expect(await screen.findByText("The allocation key was not saved")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-ALLOCATION-KEY-EXISTS/)).toBeInTheDocument();
  });
});

describe("Allocation key page", () => {
  it("shows the items, edits under If-Match and shows the new version", async () => {
    const fetchMock = stubApi([
      "/forecast-allocation-keys/FAM-A",
      (_url, init) => (init?.method === "PUT" ? json({ ...famA, version: 2, name: "Renamed" }) : json(famA)),
    ]);
    renderAt("/planning/allocation-keys/FAM-A");
    const name = await screen.findByLabelText("Name");
    await act(async () => {});
    const table = await findTable({ name: "Key items" });
    expect(within(table).getByText(ITEM_B)).toBeInTheDocument();
    expect(screen.queryByLabelText("Code")).not.toBeInTheDocument();
    fireEvent.change(name, { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved. Version 2.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(put?.[1]?.body)).name).toBe("Renamed");
  });

  it("offers a reload when someone else changed the key", async () => {
    let gets = 0;
    stubApi([
      "/forecast-allocation-keys/FAM-A",
      (_url, init) => {
        if (init?.method === "PUT") return problem(412, "conflict_version", "the allocation key was changed by another user; current version is 2");
        gets += 1;
        return json(gets === 1 ? famA : { ...famA, version: 2 });
      },
    ]);
    renderAt("/planning/allocation-keys/FAM-A");
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this allocation key")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload key" }));
    await waitFor(() => expect(gets).toBe(2));
  });

  it("shows a missing key", async () => {
    stubApi(["/forecast-allocation-keys/NOPE", () => problem(404, "PLAN-ALLOCATION-KEY-NOT-FOUND", "allocation key not found")]);
    renderAt("/planning/allocation-keys/NOPE");
    expect(await screen.findByText("Could not load allocation key NOPE")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });

  it("allocates a forecast into a model that does not exist yet (If-Match 0) and reports the cells", async () => {
    const model = { code: "MAIN", version: 1, reductionPrinciple: "transactions", reductionPercent: "0", status: "draft", submittedBy: null, decidedBy: null, publishedAt: null };
    const fetchMock = stubApi(
      ["/forecasts/MAIN/lines", () => problem(404, "PLAN-FORECAST-NOT-FOUND", "forecast model not found")],
      ["/forecasts/MAIN:allocate", () => json({ model, keyCode: "FAM-A", keyVersion: 1, items: 2, periods: 2, cellsWritten: 4 })],
      ["/forecast-allocation-keys/FAM-A", () => json(famA)],
    );
    renderAt("/planning/allocation-keys/FAM-A");
    await screen.findByRole("button", { name: "Allocate forecast" });
    fireEvent.click(screen.getByRole("button", { name: "Allocate forecast" }));
    expect(await screen.findByText("Enter the first day of the first month.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([u]) => String(u).includes(":allocate"))).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Forecast model"), { target: { value: "main" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("First month"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Monthly quantities (CASE)"), { target: { value: "100, 10.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Allocate forecast" }));
    expect(await screen.findByText("Forecast model MAIN: 4 manual cells written for 2 items over 2 months. The model is at version 1.")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([u]) => String(u).includes(":allocate"));
    expect(post?.[0]).toBe("/api/v1/planning/forecasts/MAIN:allocate");
    expect((post?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"0"');
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({
      keyCode: "FAM-A",
      siteId: SITE,
      periods: [
        { periodStart: "2027-01-01", periodEnd: "2027-01-31", qty: "100" },
        { periodStart: "2027-02-01", periodEnd: "2027-02-28", qty: "10.5" },
      ],
    });
  });

  it("explains a locked model", async () => {
    const model = { code: "MAIN", version: 3, reductionPrinciple: "transactions", reductionPercent: "0", status: "submitted" };
    stubApi(
      ["/forecasts/MAIN/lines", () => json({ model, items: [] })],
      ["/forecasts/MAIN:allocate", () => problem(409, "PLAN-FORECAST-LOCKED", "forecast model MAIN is submitted; its cells are locked")],
      ["/forecast-allocation-keys/FAM-A", () => json(famA)],
    );
    renderAt("/planning/allocation-keys/FAM-A");
    await screen.findByRole("button", { name: "Allocate forecast" });
    fireEvent.change(screen.getByLabelText("Forecast model"), { target: { value: "MAIN" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("First month"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Monthly quantities (CASE)"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Allocate forecast" }));
    expect(await screen.findByText("That forecast model is submitted or published; its cells are locked")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-FORECAST-LOCKED/)).toBeInTheDocument();
  });
});
