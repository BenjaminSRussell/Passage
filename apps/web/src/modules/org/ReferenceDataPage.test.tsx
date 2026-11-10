import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReferenceDataPage } from "./ReferenceDataPage";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (code: string, status: number, detail = "failed") => json({ code, detail, title: detail, status }, status);

type Reply = (init?: RequestInit) => Response;

function serve(routes: Record<string, Reply>) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${url.replace("/api/v1/org", "")}`;
    const match = Object.keys(routes)
      .sort((a, b) => b.length - a.length)
      .find((prefix) => key.startsWith(prefix));
    return match ? routes[match](init) : json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const callsTo = (fetchMock: ReturnType<typeof serve>, method: string, path: string) =>
  fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? "GET") === method && String(url).includes(path));

function renderPage(duties: string[] = ["org.read", "org.reference.write", "tenant.administer"]) {
  const router = createMemoryRouter(
    [{ element: createElement(OrgLayout, { duties }), children: [{ path: "/org/reference-data", element: createElement(ReferenceDataPage) }] }],
    { initialEntries: ["/org/reference-data"] },
  );
  return { user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}

const countries = [
  { code: "US", name: "United States", status: "active" },
  { code: "DE", name: "Germany", status: "inactive" },
];
const units = [
  { code: "kg", name: "Kilogram", class: "mass", decimalPrecision: 3, system: true, status: "active" },
  { code: "g", name: "Gram", class: "mass", decimalPrecision: 0, system: true, status: "active" },
  { code: "m", name: "Metre", class: "length", decimalPrecision: 2, system: true, status: "active" },
];
const conversion = {
  id: "c1",
  fromCode: "kg",
  toCode: "g",
  class: "mass",
  factor: "1000",
  offset: "0",
  inverseFactor: "0.001",
  inverseOffset: "0",
  roundTrip: true,
  system: true,
  status: "active",
};

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

describe("tabs", () => {
  it("loads only the selected tab", async () => {
    const fetchMock = serve({ "GET /countries": () => json({ items: countries }) });
    const { user } = renderPage();
    await screen.findByText("United States");
    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Countries",
      "Currencies",
      "Regions",
      "Languages",
      "Units",
      "Conversions",
      "Rounding",
    ]);
    expect(callsTo(fetchMock, "GET", "/countries")).toHaveLength(1);
    expect(callsTo(fetchMock, "GET", "/currencies")).toHaveLength(0);
    expect(callsTo(fetchMock, "GET", "/uom/")).toHaveLength(0);
    await user.click(screen.getByRole("tab", { name: "Currencies" }));
    await findTable({ name: "Currencies" });
    expect(callsTo(fetchMock, "GET", "/currencies")).toHaveLength(1);
    expect(callsTo(fetchMock, "GET", "/countries")).toHaveLength(1);
  });

  it("moves between tabs with the arrow keys", async () => {
    serve({});
    const { user } = renderPage();
    await user.click(await screen.findByRole("tab", { name: "Countries" }));
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Currencies" })).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{End}");
    expect(screen.getByRole("tab", { name: "Rounding" })).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Countries" })).toHaveAttribute("aria-selected", "true");
  });
});

describe("reference lists", () => {
  it("lists rows with status pills and filters by status", async () => {
    const fetchMock = serve({ "GET /countries": () => json({ items: countries }) });
    const { user } = renderPage();
    const table = await findTable({ name: "Countries" });
    await within(table).findByText("Germany");
    expect(within(table).getByText("Inactive")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Filter by status"), "inactive");
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/countries?status=inactive")).toHaveLength(1));
  });

  it("shows the problem code when the list cannot load", async () => {
    serve({ "GET /countries": () => problem("AUTH-0003", 403, "no duty") });
    renderPage();
    expect(await screen.findByText(/no duty/)).toBeInTheDocument();
  });

  it("deactivates after confirmation and shows the response status", async () => {
    const fetchMock = serve({
      "GET /countries": () => json({ items: countries }),
      "PATCH /countries/US": () => json({ ...countries[0], status: "inactive" }),
    });
    const { user } = renderPage();
    await user.click(await screen.findByRole("button", { name: "Deactivate US" }));
    expect(callsTo(fetchMock, "PATCH", "/countries")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Confirm deactivate US" }));
    await screen.findByRole("button", { name: "Activate US" });
    const [, init] = callsTo(fetchMock, "PATCH", "/countries/US")[0];
    expect(init?.body).toBe(JSON.stringify({ status: "inactive" }));
    expect(screen.queryByRole("button", { name: /delete/i })).not.toBeInTheDocument();
  });

  it("sends nothing when the confirmation is cancelled", async () => {
    const fetchMock = serve({ "GET /countries": () => json({ items: countries }) });
    const { user } = renderPage();
    await user.click(await screen.findByRole("button", { name: "Deactivate US" }));
    expect(screen.getByRole("button", { name: "Confirm deactivate US" })).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Cancel deactivate US" }));
    expect(screen.getByRole("button", { name: "Deactivate US" })).toHaveFocus();
    expect(callsTo(fetchMock, "PATCH", "/countries")).toHaveLength(0);
  });

  it("keeps the row unchanged and shows the code when the PATCH fails", async () => {
    serve({
      "GET /countries": () => json({ items: countries }),
      "PATCH /countries/US": () => problem("AUTH-0003", 403, "not allowed"),
    });
    const { user } = renderPage();
    await user.click(await screen.findByRole("button", { name: "Deactivate US" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate US" }));
    expect(await screen.findByText(/not allowed/)).toBeInTheDocument();
    const row = screen.getByRole("row", { name: /United States/ });
    expect(within(row).getByText("Active")).toBeInTheDocument();
  });

  it("renders no action buttons without org.reference.write", async () => {
    serve({ "GET /countries": () => json({ items: countries }) });
    renderPage(["org.read"]);
    await screen.findByText("United States");
    expect(screen.queryByRole("button", { name: /activate|delete/i })).not.toBeInTheDocument();
  });

  it("renders no action buttons with org.reference.write but without tenant.administer", async () => {
    serve({ "GET /countries": () => json({ items: countries }) });
    renderPage(["org.read", "org.reference.write"]);
    await screen.findByText("United States");
    expect(screen.queryByRole("button", { name: /activate|delete/i })).not.toBeInTheDocument();
  });

  it("keeps regions that share a code apart", async () => {
    serve({
      "GET /regions": () =>
        json({
          items: [
            { code: "01", name: "North", countryCode: "US", status: "active" },
            { code: "01", name: "Nord", countryCode: "DE", status: "active" },
          ],
        }),
    });
    const { user } = renderPage();
    await user.click(await screen.findByRole("tab", { name: "Regions" }));
    const table = await findTable({ name: "Regions" });
    await within(table).findByText("North");
    expect(within(table).getByText("Nord")).toBeInTheDocument();
  });
});

describe("units tab", () => {
  const unitRoutes = { "GET /uom/units": () => json({ items: units }) };

  async function openUnits(duties?: string[]) {
    const view = renderPage(duties);
    await view.user.click(await screen.findByRole("tab", { name: "Units" }));
    await screen.findByText("Kilogram");
    return view;
  }

  it("lists units with origin and hides the form without the duty", async () => {
    serve(unitRoutes);
    await openUnits(["org.read"]);
    expect(screen.getAllByText("System").length).toBeGreaterThan(0);
    expect(screen.queryByRole("heading", { name: "Add custom unit" })).not.toBeInTheDocument();
  });

  it("deactivates a platform unit through the unit route", async () => {
    const fetchMock = serve({ ...unitRoutes, "PATCH /uom/units/m": () => json({ ...units[2], status: "inactive" }) });
    const { user } = await openUnits();
    await user.click(screen.getByRole("button", { name: "Deactivate m" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate m" }));
    await screen.findByRole("button", { name: "Activate m" });
    expect(callsTo(fetchMock, "PATCH", "/uom/units/m")).toHaveLength(1);
  });

  it("validates before sending", async () => {
    const fetchMock = serve(unitRoutes);
    const { user } = await openUnits();
    await user.type(screen.getByLabelText("Code"), "bad code!");
    await user.clear(screen.getByLabelText("Decimal precision"));
    await user.type(screen.getByLabelText("Decimal precision"), "7");
    await user.click(screen.getByRole("button", { name: "Add unit" }));
    expect(await screen.findByText("Use 1 to 10 letters, digits, / or %.")).toBeInTheDocument();
    expect(screen.getByText("Enter a name of 1 to 120 characters.")).toBeInTheDocument();
    expect(screen.getByText("Enter a whole number from 0 to 6.")).toBeInTheDocument();
    expect(callsTo(fetchMock, "POST", "/uom/units")).toHaveLength(0);
  });

  it("posts a custom unit and refreshes the table", async () => {
    const fetchMock = serve({ ...unitRoutes, "POST /uom/units": () => json({ ...units[2], code: "m/s" }, 201) });
    const { user } = await openUnits();
    await user.type(screen.getByLabelText("Code"), "m/s");
    await user.type(screen.getByLabelText("Name"), "  Metre per second ");
    await user.selectOptions(screen.getByLabelText("Class"), "speed");
    await user.clear(screen.getByLabelText("Decimal precision"));
    await user.type(screen.getByLabelText("Decimal precision"), "3");
    await user.click(screen.getByRole("button", { name: "Add unit" }));
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/uom/units")).toHaveLength(2));
    const [, init] = callsTo(fetchMock, "POST", "/uom/units")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ code: "m/s", name: "Metre per second", class: "speed", decimalPrecision: 3 });
  });

  it("shows a duplicate code as a field error on code", async () => {
    serve({ ...unitRoutes, "POST /uom/units": () => problem("ORG-UOM-CODE-DUPLICATE", 409, "Code is taken") });
    const { user } = await openUnits();
    await user.type(screen.getByLabelText("Code"), "kg");
    await user.type(screen.getByLabelText("Name"), "Kilo");
    await user.click(screen.getByRole("button", { name: "Add unit" }));
    const field = await screen.findByText("Code is taken");
    expect(field.closest("p")).toHaveAttribute("id", "unit-code-error");
    expect(screen.getByLabelText("Code")).toHaveValue("kg");
  });
});

describe("conversions tab", () => {
  const conversionRoutes = {
    "GET /uom/units": () => json({ items: units }),
    "GET /uom/conversions": () => json({ items: [conversion] }),
  };

  async function openConversions(duties?: string[]) {
    const view = renderPage(duties);
    await view.user.click(await screen.findByRole("tab", { name: "Conversions" }));
    await screen.findByRole("cell", { name: "0.001" });
    await screen.findAllByRole("option", { name: "kg - Kilogram" });
    return view;
  }

  it("rejects equal units and malformed numbers without sending", async () => {
    const fetchMock = serve(conversionRoutes);
    const { user } = await openConversions();
    await user.selectOptions(screen.getByLabelText("From unit"), "kg");
    await user.selectOptions(screen.getByLabelText("To unit"), "kg");
    await user.type(screen.getByLabelText("Factor"), "1e5");
    await user.type(screen.getByLabelText(/Offset/), "+5");
    await user.click(screen.getByRole("button", { name: "Add conversion" }));
    expect(await screen.findByText("Choose a different unit from the from unit.")).toBeInTheDocument();
    expect(screen.getByText(/Enter a positive decimal/)).toBeInTheDocument();
    expect(screen.getByText(/Enter a signed decimal/)).toBeInTheDocument();
    expect(callsTo(fetchMock, "POST", "/uom/conversions")).toHaveLength(0);
  });

  it("rejects a zero factor", async () => {
    serve(conversionRoutes);
    const { user } = await openConversions();
    await user.type(screen.getByLabelText("Factor"), "0.000");
    await user.click(screen.getByRole("button", { name: "Add conversion" }));
    expect(await screen.findByText(/Enter a positive decimal/)).toBeInTheDocument();
  });

  it("posts the conversion and refreshes the table", async () => {
    const fetchMock = serve({ ...conversionRoutes, "POST /uom/conversions": () => json(conversion, 201) });
    const { user } = await openConversions();
    await user.selectOptions(screen.getByLabelText("From unit"), "kg");
    await user.selectOptions(screen.getByLabelText("To unit"), "g");
    await user.type(screen.getByLabelText("Factor"), "1000.5");
    await user.click(screen.getByRole("button", { name: "Add conversion" }));
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/uom/conversions")).toHaveLength(2));
    const [, init] = callsTo(fetchMock, "POST", "/uom/conversions")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ fromCode: "kg", toCode: "g", factor: "1000.5" });
  });

  it.each([
    ["ORG-UOM-UNIT-NOT-FOUND", "One of the selected units does not exist."],
    ["ORG-UOM-CONVERSION-DUPLICATE", "A conversion between these two units already exists."],
    ["ORG-UOM-UNIT-INACTIVE", "One of the selected units is inactive."],
    ["ORG-UOM-CLASS-MISMATCH", "The two units belong to different classes."],
  ])("renders %s as a readable message with the code", async (code, message) => {
    serve({ ...conversionRoutes, "POST /uom/conversions": () => problem(code, 409) });
    const { user } = await openConversions();
    await user.selectOptions(screen.getByLabelText("From unit"), "kg");
    await user.selectOptions(screen.getByLabelText("To unit"), "m");
    await user.type(screen.getByLabelText("Factor"), "2");
    await user.click(screen.getByRole("button", { name: "Add conversion" }));
    expect(await screen.findByText(`${message} (${code})`)).toBeInTheDocument();
  });

  it("hides the add form without the duty but keeps the test panel", async () => {
    serve(conversionRoutes);
    await openConversions(["org.read"]);
    expect(screen.queryByRole("heading", { name: "Add conversion" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Test conversion" })).toBeInTheDocument();
  });

  async function runTest(user: ReturnType<typeof userEvent.setup>, quantity: string, to = "g") {
    await user.selectOptions(screen.getByLabelText("Test from unit"), "kg");
    await user.selectOptions(screen.getByLabelText("Test to unit"), to);
    await user.clear(screen.getByLabelText("Quantity"));
    await user.type(screen.getByLabelText("Quantity"), quantity);
    await user.click(screen.getByRole("button", { name: "Convert" }));
  }

  it("shows the returned quantities verbatim for a read-only user", async () => {
    const fetchMock = serve({
      ...conversionRoutes,
      "GET /uom/convert": () => json({ from: "kg", to: "g", quantity: "1234.567890123456", displayQuantity: "1234.568" }),
    });
    const { user } = await openConversions(["org.read"]);
    await runTest(user, "1.234567890123");
    expect(await screen.findByText("1234.567890123456")).toBeInTheDocument();
    expect(screen.getByText("1234.568")).toBeInTheDocument();
    expect(String(callsTo(fetchMock, "GET", "/uom/convert")[0][0])).toContain("qty=1.234567890123");
  });

  it("shows the catalog message for an unreachable pair", async () => {
    serve({ ...conversionRoutes, "GET /uom/convert": () => problem("ORG-UOM-NO-PATH", 422, "server text") });
    const { user } = await openConversions();
    await runTest(user, "5", "m");
    expect(await screen.findByText("No conversion from kg to m within two steps.")).toBeInTheDocument();
  });

  it.each(["", "NaN", "1e3", "abc", "1,5"])("rejects the quantity %j without sending", async (quantity) => {
    const fetchMock = serve(conversionRoutes);
    const { user } = await openConversions();
    await user.selectOptions(screen.getByLabelText("Test from unit"), "kg");
    await user.selectOptions(screen.getByLabelText("Test to unit"), "g");
    if (quantity) {
      await user.type(screen.getByLabelText("Quantity"), quantity);
    }
    await user.click(screen.getByRole("button", { name: "Convert" }));
    expect(await screen.findByText("Enter a decimal number such as 12.5.")).toBeInTheDocument();
    expect(callsTo(fetchMock, "GET", "/uom/convert")).toHaveLength(0);
  });
});
