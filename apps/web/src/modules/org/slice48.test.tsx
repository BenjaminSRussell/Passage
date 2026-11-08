import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findTable } from "../../test-utils";
import { NameTranslationsPage } from "./NameTranslationsPage";
import { ReferenceDataPage } from "./ReferenceDataPage";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";
import { validateAmount, validateRule } from "./RoundingTab";
import { validateTranslation } from "./NameTranslationsPage";

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

const entity = { id: "le1", code: "USMF", name: "Contoso US", country: "US", currency: "USD", timeZone: "UTC", defaultLanguage: "en", status: "active", version: 1 };
const usd = { code: "USD", name: "US dollar", numericCode: "840", minorUnits: 2, status: "active" };
const rule = { id: "r1", currencyCode: "USD", purpose: "cash", legalEntityId: null, unit: "0.05", method: "half_up", updatedAt: "2026-11-01T10:00:00Z" };

function renderAt(path: string, element: ReturnType<typeof createElement>, duties: string[]) {
  const router = createMemoryRouter([{ element: createElement(OrgLayout, { duties }), children: [{ path, element }] }], { initialEntries: [path] });
  return { user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

describe("validators", () => {
  it("checks the rounding unit, amount and translation", () => {
    expect(validateRule("0.05")).toBeUndefined();
    expect(validateRule("0")).toBeDefined();
    expect(validateRule("0.0000001")).toBeDefined();
    expect(validateAmount("-10.075")).toBeUndefined();
    expect(validateAmount("ten")).toBeDefined();
    expect(validateTranslation("fr-CA", "Contoso")).toEqual({});
    expect(validateTranslation("FR", "  ")).toEqual({ language: expect.any(String), name: expect.any(String) });
  });
});

async function openRounding(duties: string[], extra: Record<string, Reply> = {}) {
  const fetchMock = serve({
    "GET /legal-entities": () => json({ items: [entity] }),
    "GET /currencies?status=active": () => json({ items: [usd] }),
    "GET /currencies/USD/rounding-rules": () => json({ items: [rule] }),
    ...extra,
  });
  const view = renderAt("/org/reference-data", createElement(ReferenceDataPage), duties);
  await view.user.click(await screen.findByRole("tab", { name: "Rounding" }));
  return { fetchMock, ...view };
}

describe("rounding rules tab", () => {
  it("asks for a currency first, then lists its rules", async () => {
    const { user } = await openRounding(["org.read"]);
    expect(await screen.findByRole("heading", { name: "Select a currency" })).toBeInTheDocument();
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    const table = await findTable({ name: "Rounding rules" });
    expect(within(table).getByText("0.05")).toBeInTheDocument();
    expect(within(table).getByText("Whole tenant")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save rule" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Remove/ })).not.toBeInTheDocument();
  });

  it("reads the rules under the switcher scope, since legal-entity rules are behind the legal-entity policy", async () => {
    const { user, fetchMock } = await openRounding(["org.read"]);
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    await findTable({ name: "Rounding rules" });
    const headers = new Headers(callsTo(fetchMock, "GET", "/rounding-rules")[0][1]?.headers);
    expect(headers.get("X-Passage-LegalEntity")).toBe("le1");
    expect(screen.getByText(/Showing whole-tenant rules and rules for USMF/)).toBeInTheDocument();
  });

  it("reads every entity's rules under the ALL scope", async () => {
    stubLocalStorage().setItem("passage.org.legalEntity", "ALL");
    const { user, fetchMock } = await openRounding(["org.read", "org.switcher.all-scope"]);
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    await findTable({ name: "Rounding rules" });
    const headers = new Headers(callsTo(fetchMock, "GET", "/rounding-rules")[0][1]?.headers);
    expect(headers.get("X-Passage-LegalEntity-Scope")).toBe("ALL");
    expect(headers.get("X-Passage-LegalEntity")).toBeNull();
    expect(screen.queryByText(/Showing whole-tenant rules/)).not.toBeInTheDocument();
  });

  it("sends writes tenant-level, without a legal-entity header", async () => {
    const { user, fetchMock } = await openRounding(["org.read", "org.reference.write", "tenant.administer"], {
      "PUT /currencies/USD/rounding-rules/general": () => json({ ...rule, purpose: "general", unit: "0.01", legalEntityId: "le1" }),
      "DELETE /currencies/USD/rounding-rules/cash": () => new Response(null, { status: 204 }),
    });
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    await user.type(await screen.findByLabelText("Rounding unit"), "0.01");
    await user.selectOptions(screen.getByLabelText("Applies to"), "le1");
    await user.click(screen.getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(callsTo(fetchMock, "PUT", "/rounding-rules/general")).toHaveLength(1));
    const put = new Headers(callsTo(fetchMock, "PUT", "/rounding-rules/general")[0][1]?.headers);
    expect(put.get("X-Passage-LegalEntity")).toBeNull();
    expect(put.get("X-Passage-LegalEntity-Scope")).toBeNull();
    await user.click(await screen.findByRole("button", { name: "Remove cash rule" }));
    await user.click(screen.getByRole("button", { name: "Confirm remove cash rule" }));
    await waitFor(() => expect(callsTo(fetchMock, "DELETE", "/rounding-rules/cash")).toHaveLength(1));
    expect(new Headers(callsTo(fetchMock, "DELETE", "/rounding-rules/cash")[0][1]?.headers).get("X-Passage-LegalEntity")).toBeNull();
  });

  it("shows an empty state and a load error", async () => {
    const { user } = await openRounding(["org.read"], { "GET /currencies/USD/rounding-rules": () => json({ items: [] }) });
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    expect(await screen.findByText("No rounding rules")).toBeInTheDocument();
  });

  it("reports a failed load", async () => {
    const { user } = await openRounding(["org.read"], { "GET /currencies/USD/rounding-rules": () => problem("ORG-REF-NOT-FOUND", 404, "unknown currency") });
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    expect(await screen.findByText(/unknown currency \(ORG-REF-NOT-FOUND\)/)).toBeInTheDocument();
  });

  it("saves a rule and reloads, showing the unit error from the service", async () => {
    let puts = 0;
    const { user, fetchMock } = await openRounding(["org.read", "org.reference.write", "tenant.administer"], {
      "PUT /currencies/USD/rounding-rules/price": () => {
        puts += 1;
        return puts === 1 ? problem("ORG-CUR-ROUNDING-UNIT", 422, "bad unit") : json({ ...rule, purpose: "price", unit: "0.01" });
      },
    });
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    await user.click(await screen.findByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/Enter a positive decimal/)).toBeInTheDocument();
    expect(callsTo(fetchMock, "PUT", "rounding-rules")).toHaveLength(0);

    await user.selectOptions(screen.getByLabelText("Purpose"), "price");
    await user.type(screen.getByLabelText("Rounding unit"), "0.3");
    await user.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/power of ten/)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Rounding unit"));
    await user.type(screen.getByLabelText("Rounding unit"), "0.01");
    await user.selectOptions(screen.getByLabelText("Applies to"), "le1");
    await user.click(screen.getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(callsTo(fetchMock, "PUT", "rounding-rules/price")).toHaveLength(2));
    const body = JSON.parse(String(callsTo(fetchMock, "PUT", "rounding-rules/price")[1][1]?.body));
    expect(body).toEqual({ unit: "0.01", method: "half_even", legalEntityId: "le1" });
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/rounding-rules")).toHaveLength(2));
  });

  it("removes a rule after confirmation", async () => {
    const { user, fetchMock } = await openRounding(["org.read", "org.reference.write", "tenant.administer"], {
      "DELETE /currencies/USD/rounding-rules/cash": () => new Response(null, { status: 204 }),
    });
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    await user.click(await screen.findByRole("button", { name: "Remove cash rule" }));
    expect(callsTo(fetchMock, "DELETE", "rounding-rules")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Confirm remove cash rule" }));
    await waitFor(() => expect(callsTo(fetchMock, "DELETE", "/rounding-rules/cash")).toHaveLength(1));
  });

  it("shows the rule the test amount used", async () => {
    const { user, fetchMock } = await openRounding(["org.read"], {
      "GET /currencies/USD/round": () => json({ currency: "USD", purpose: "cash", legalEntityId: null, amount: "10.07", rounded: "10.05", unit: "0.05", method: "half_up", source: "tenant" }),
    });
    await user.selectOptions(await screen.findByLabelText("Currency"), "USD");
    await user.type(await screen.findByLabelText("Amount"), "x");
    await user.click(screen.getByRole("button", { name: "Round" }));
    expect(await screen.findByText(/Enter a decimal amount/)).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Amount"));
    await user.type(screen.getByLabelText("Amount"), "10.07");
    await user.selectOptions(screen.getByLabelText("Test purpose"), "cash");
    await user.click(screen.getByRole("button", { name: "Round" }));
    expect(await screen.findByText("10.05")).toBeInTheDocument();
    expect(screen.getByText("Tenant rule")).toBeInTheDocument();
    const tenantCall = callsTo(fetchMock, "GET", "/round?")[0];
    expect(String(tenantCall[0])).toContain("amount=10.07&purpose=cash");
    expect(String(tenantCall[0])).not.toContain("legalEntityId");
    expect(new Headers(tenantCall[1]?.headers).get("X-Passage-LegalEntity")).toBeNull();

    await user.selectOptions(screen.getByLabelText("Test legal entity"), "le1");
    await user.click(screen.getByRole("button", { name: "Round" }));
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/round?")).toHaveLength(2));
    const entityCall = callsTo(fetchMock, "GET", "/round?")[1];
    expect(String(entityCall[0])).toContain("legalEntityId=le1");
    expect(new Headers(entityCall[1]?.headers).get("X-Passage-LegalEntity")).toBe("le1");
  });
});

const translation = { ownerType: "legal_entity", ownerId: "le1", language: "fr", name: "Contoso Etats-Unis", createdAt: "2026-11-01T10:00:00Z", updatedAt: "2026-11-02T10:00:00Z" };

async function openNames(duties: string[], extra: Record<string, Reply> = {}) {
  const fetchMock = serve({
    "GET /legal-entities?": () => json({ items: [entity] }),
    "GET /legal-entities/le1/translations": () => json({ items: [translation] }),
    ...extra,
  });
  const view = renderAt("/org/names", createElement(NameTranslationsPage), duties);
  await view.user.selectOptions(await screen.findByLabelText("Record"), "le1");
  return { fetchMock, ...view };
}

describe("name translations page", () => {
  it("asks for a record, then lists its translations read-only without the write duty", async () => {
    serve({ "GET /legal-entities?": () => json({ items: [entity] }) });
    const view = renderAt("/org/names", createElement(NameTranslationsPage), ["org.read"]);
    expect(await screen.findByRole("heading", { name: "Select a record" })).toBeInTheDocument();
    view.unmount();
    await openNames(["org.read"]);
    const table = await findTable({ name: "Name translations" });
    expect(within(table).getByText("Contoso Etats-Unis")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save translation" })).not.toBeInTheDocument();
  });

  it("sends the legal-entity header and reports a failed load", async () => {
    const { fetchMock } = await openNames(["org.read"], { "GET /legal-entities/le1/translations": () => problem("ORG-LE-NOT-FOUND", 404, "not found") });
    expect(await screen.findByText(/not found \(ORG-LE-NOT-FOUND\)/)).toBeInTheDocument();
    const call = callsTo(fetchMock, "GET", "/legal-entities/le1/translations")[0];
    expect(new Headers(call[1]?.headers).get("X-Passage-LegalEntity")).toBe("le1");
  });

  it("shows the empty state", async () => {
    await openNames(["org.read"], { "GET /legal-entities/le1/translations": () => json({ items: [] }) });
    expect(await screen.findByText("No translations")).toBeInTheDocument();
  });

  it("validates, saves and edits a translation", async () => {
    const { user, fetchMock } = await openNames(["org.read", "org.legal-entity.write"], {
      "PUT /legal-entities/le1/translations/de": () => json({ ...translation, language: "de", name: "Contoso USA" }, 201),
      "PUT /legal-entities/le1/translations/xx": () => problem("ORG-TRANSLATION-LANGUAGE-INVALID", 422, "inactive"),
    });
    await user.click(await screen.findByRole("button", { name: "Save translation" }));
    expect(await screen.findByText(/Enter a language tag/)).toBeInTheDocument();
    expect(callsTo(fetchMock, "PUT", "/translations")).toHaveLength(0);

    await user.type(screen.getByLabelText("Language"), "xx");
    await user.type(screen.getByLabelText("Translated name"), "Nope");
    await user.click(screen.getByRole("button", { name: "Save translation" }));
    expect(await screen.findByText(/not an active language/)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Language"));
    await user.type(screen.getByLabelText("Language"), "de");
    await user.click(screen.getByRole("button", { name: "Save translation" }));
    await waitFor(() => expect(callsTo(fetchMock, "PUT", "/translations/de")).toHaveLength(1));
    expect(JSON.parse(String(callsTo(fetchMock, "PUT", "/translations/de")[0][1]?.body))).toEqual({ name: "Nope" });
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/translations")).toHaveLength(2));

    await user.click(await screen.findByRole("button", { name: "Edit fr name" }));
    expect(screen.getByLabelText("Language")).toHaveValue("fr");
    expect(screen.getByLabelText("Translated name")).toHaveValue("Contoso Etats-Unis");
  });

  it("removes a translation after confirmation", async () => {
    const { user, fetchMock } = await openNames(["org.read", "org.legal-entity.write"], {
      "DELETE /legal-entities/le1/translations/fr": () => new Response(null, { status: 204 }),
    });
    await user.click(await screen.findByRole("button", { name: "Remove fr name" }));
    await user.click(screen.getByRole("button", { name: "Confirm remove fr name" }));
    await waitFor(() => expect(callsTo(fetchMock, "DELETE", "/translations/fr")).toHaveLength(1));
  });

  it("is read-only under the ALL scope even with the write duty", async () => {
    stubLocalStorage().setItem("passage.org.legalEntity", "ALL");
    const { fetchMock } = await openNames(["org.read", "org.legal-entity.write", "org.switcher.all-scope"]);
    expect(await screen.findByText("Contoso Etats-Unis")).toBeInTheDocument();
    expect(screen.getByText(/Choose one legal entity in the switcher/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save translation" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove fr name" })).not.toBeInTheDocument();
    const headers = new Headers(callsTo(fetchMock, "GET", "/legal-entities/le1/translations")[0][1]?.headers);
    expect(headers.get("X-Passage-LegalEntity-Scope")).toBe("ALL");
  });

  it("gates site and warehouse writes on org.site.write", async () => {
    const site = { id: "s1", code: "S1", name: "Main site", legalEntityId: "le1", status: "active", version: 1 };
    serve({
      "GET /legal-entities?": () => json({ items: [entity] }),
      "GET /sites?": () => json({ items: [site] }),
      "GET /sites/s1/translations": () => json({ items: [] }),
    });
    const { user } = renderAt("/org/names", createElement(NameTranslationsPage), ["org.read", "org.legal-entity.write"]);
    await user.selectOptions(await screen.findByLabelText("Record type"), "sites");
    await user.selectOptions(await screen.findByRole("combobox", { name: "Record" }), "s1");
    expect(await screen.findByText("No translations")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save translation" })).not.toBeInTheDocument();
  });

  it("previews the name a language resolves to", async () => {
    await openNames(["org.read"], {
      "GET /legal-entities/le1/name": () => json({ ownerType: "legal_entity", ownerId: "le1", language: "fr", name: "Contoso Etats-Unis", source: "translation" }),
    });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Preview language"), "fr-CA");
    await user.click(screen.getByRole("button", { name: "Preview name" }));
    expect(await screen.findByText("Translation (fr)")).toBeInTheDocument();
  });
});
