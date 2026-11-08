import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { copySummary } from "./CopyLegalEntityForm";
import { LegalEntitiesPage } from "./LegalEntityAdmin";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (code: string, status: number) => json({ code, detail: "failed", title: "failed", status }, status);
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

function renderPage(duties: string[]) {
  const router = createMemoryRouter([{ element: createElement(OrgLayout, { duties }), children: [{ path: "/org/legal-entities", element: createElement(LegalEntitiesPage) }] }], {
    initialEntries: ["/org/legal-entities"],
  });
  return { user: userEvent.setup({ delay: null }), ...render(<RouterProvider router={router} />) };
}

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

const entity = { id: "le-1", code: "USMF", name: "Contoso US", country: "US", currency: "USD", timeZone: "America/Detroit", status: "active", version: 2 };
const lookups = {
  "GET /legal-entities": () => json({ items: [entity] }),
  "GET /countries": () => json({ items: [{ code: "CA", name: "Canada", status: "active" }] }),
  "GET /currencies": () => json({ items: [{ code: "CAD", name: "Canadian Dollar", status: "active" }] }),
};
const ADMIN = ["org.read", "org.legal-entity.write", "tenant.administer"];
const openedForm = () => screen.findByRole("form", { name: "Copy legal entity USMF" });
// fill sets a text field in one change event: typing key by key re-renders the whole form per character.
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const calls = (fetchMock: ReturnType<typeof serve>, method: string, suffix: string) =>
  fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? "GET") === method && String(url).includes(suffix));

describe("copy legal entity", () => {
  it("is offered only to a tenant administrator who can write legal entities", async () => {
    serve(lookups);
    const first = renderPage(["org.read", "org.legal-entity.write"]);
    await findTable({ name: "Legal entities" });
    await screen.findByRole("button", { name: "Deactivate legal entity USMF" });
    expect(screen.queryByRole("button", { name: "Copy legal entity USMF" })).toBeNull();
    first.unmount();
    renderPage(["org.read", "tenant.administer"]);
    await findTable({ name: "Legal entities" });
    expect(screen.queryByRole("button", { name: /Copy legal entity/ })).toBeNull();
  });

  it("checks the form, copies with the chosen options and reports what was created", async () => {
    const fetchMock = serve({
      ...lookups,
      "POST /legal-entities/le-1:copy": () =>
        json({ legalEntity: { ...entity, id: "le-2", code: "CAMF" }, sourceId: "le-1", sites: 2, warehouses: 5, locations: 3, roundingRules: 0, calendarCopied: true }, 201),
    });
    const { user } = renderPage(ADMIN);
    await findTable({ name: "Legal entities" });
    await user.click(await screen.findByRole("button", { name: "Copy legal entity USMF" }));
    expect(screen.getByLabelText("New time zone")).toHaveValue("America/Detroit");

    await user.click(screen.getByRole("button", { name: "Copy legal entity" }));
    expect(await screen.findByText("Use 2 to 8 letters or digits.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":copy")).toHaveLength(0);

    const form = await openedForm();
    await within(form).findByRole("option", { name: "CA - Canada" });
    await within(form).findByRole("option", { name: "CAD - Canadian Dollar" });
    fill("New code", "camf");
    fill("New name", "Contoso Canada");
    await user.selectOptions(screen.getByLabelText("New country"), "CA");
    await user.selectOptions(screen.getByLabelText("New accounting currency"), "CAD");
    await user.click(screen.getByLabelText("Currency rounding rules"));
    await user.click(screen.getByRole("button", { name: "Copy legal entity" }));

    await waitFor(() => expect(calls(fetchMock, "POST", ":copy")).toHaveLength(1));
    const [url, init] = calls(fetchMock, "POST", ":copy")[0];
    expect(String(url)).toContain("/legal-entities/le-1:copy");
    expect(JSON.parse(String(init?.body))).toEqual({
      code: "CAMF",
      name: "Contoso Canada",
      country: "CA",
      currency: "CAD",
      timeZone: "America/Detroit",
      include: { sites: true, locations: true, roundingRules: false },
    });
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByText(/Copied USMF to CAMF: 2 sites, 5 warehouses, 3 locations/)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Copy legal entity USMF" })).toBeNull();
    await waitFor(() => expect(calls(fetchMock, "GET", "/legal-entities").length).toBeGreaterThan(1));
  });

  it("drops locations when sites are not copied", async () => {
    const fetchMock = serve({ ...lookups, "POST /legal-entities/le-1:copy": () => json({ legalEntity: entity, sourceId: "le-1", sites: 0, warehouses: 0, locations: 0, roundingRules: 0, calendarCopied: false }, 201) });
    const { user } = renderPage(ADMIN);
    await findTable({ name: "Legal entities" });
    await user.click(await screen.findByRole("button", { name: "Copy legal entity USMF" }));
    await within(await openedForm()).findByRole("option", { name: "CA - Canada" });
    fill("New code", "BARE");
    fill("New name", "Bare");
    await user.selectOptions(screen.getByLabelText("New country"), "CA");
    await user.selectOptions(screen.getByLabelText("New accounting currency"), "CAD");
    await user.click(screen.getByLabelText("Active sites and warehouses"));
    expect(screen.getByLabelText("Active locations")).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Copy legal entity" }));
    await waitFor(() => expect(calls(fetchMock, "POST", ":copy")).toHaveLength(1));
    expect(JSON.parse(String(calls(fetchMock, "POST", ":copy")[0][1]?.body)).include).toEqual({ sites: false, locations: false, roundingRules: true });
  });

  it("shows a duplicate code on the field and any other failure in an alert, and cancels", async () => {
    let reply: Reply = () => problem("ORG-LE-CODE-DUPLICATE", 409);
    serve({ ...lookups, "POST /legal-entities/le-1:copy": (init) => reply(init) });
    const { user } = renderPage(ADMIN);
    await findTable({ name: "Legal entities" });
    await user.click(await screen.findByRole("button", { name: "Copy legal entity USMF" }));
    await within(await openedForm()).findByRole("option", { name: "CA - Canada" });
    fill("New code", "USMF");
    fill("New name", "Again");
    await user.selectOptions(screen.getByLabelText("New country"), "CA");
    await user.selectOptions(screen.getByLabelText("New accounting currency"), "CAD");
    await user.click(screen.getByRole("button", { name: "Copy legal entity" }));
    expect(await screen.findByText("A legal entity with this code already exists.")).toBeInTheDocument();

    reply = () => problem("ORG-LE-REFERENCE-INACTIVE", 422);
    await user.click(screen.getByRole("button", { name: "Copy legal entity" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText(/ORG-LE-REFERENCE-INACTIVE/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Cancel copy" }));
    expect(screen.queryByLabelText("New code")).toBeNull();
    expect(screen.getByRole("button", { name: "Copy legal entity USMF" })).toBeInTheDocument();
  });

  it("summarizes a copy without a calendar", () => {
    const text = copySummary("USMF", { legalEntity: { ...entity, code: "CAMF" } as never, sourceId: "le-1", sites: 1, warehouses: 2, locations: 0, roundingRules: 1, calendarCopied: false });
    expect(text).toBe("Copied USMF to CAMF: 1 sites, 2 warehouses, 0 locations, 1 rounding rules. Number sequences and parameters are set up by their own services.");
  });
});
