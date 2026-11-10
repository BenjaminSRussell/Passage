import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";
import { LegalEntitiesPage } from "./LegalEntityAdmin";
import { validateAddress } from "./AddressAction";
import { SitesPage } from "./StructurePages";
import { TeamsPage } from "./TeamsPage";

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

function renderPage(path: string, Page: ComponentType, duties: string[]) {
  const router = createMemoryRouter([{ element: createElement(OrgLayout, { duties }), children: [{ path, element: createElement(Page) }] }], {
    initialEntries: [path],
  });
  return { user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

const entity = { id: "le-1", code: "USMF", name: "Contoso US", country: "US", currency: "USD", timeZone: "UTC", status: "active", version: 2 };
const entities = { "GET /legal-entities": () => json({ items: [entity] }) };
const site = { id: "s-1", legalEntityId: "le-1", code: "DET", name: "Detroit", timeZone: "America/Detroit", status: "active", version: 3 };
const address = {
  id: "a-1",
  versionId: "v-1",
  ownerType: "site",
  ownerId: "s-1",
  countryCode: "US",
  regionCode: "MI",
  city: "Detroit",
  postalCode: "48201",
  street1: "1 Main St",
  formatted: "1 Main St, Detroit MI 48201",
  purposes: [{ purpose: "business", primary: true }],
  validFrom: "2026-01-01",
};
// The address the service returns for the form that fill() submits.
const posted = { ...address, regionCode: "MI", postalCode: undefined, formatted: "1 Main St, Detroit MI", purposes: [{ purpose: "delivery", primary: true }] };
const SITE_ADDRESS = ["org.read", "org.site.write", "org.address.write"];
const bodyOf = (call: [unknown, RequestInit | undefined]) => JSON.parse(String(call[1]?.body));
const headerOf = (call: [unknown, RequestInit | undefined], name: string) => new Headers(call[1]?.headers).get(name);

async function openAddress(user: ReturnType<typeof userEvent.setup>, name: string) {
  const table = await findTable({ name: "Sites" });
  await user.click(within(table).getByRole("button", { name }));
  return { table, form: await screen.findByRole("form", { name: "Edit address of site DET" }) };
}

async function fill(user: ReturnType<typeof userEvent.setup>, form: HTMLElement) {
  await user.type(within(form).getByLabelText("Street"), "1 Main St");
  await user.type(within(form).getByLabelText("City"), "Detroit");
  await user.type(within(form).getByLabelText(/Region/), "mi");
  await user.type(within(form).getByLabelText("Country"), "us");
  await user.selectOptions(within(form).getByLabelText("Purpose"), "delivery");
}

describe("address validation", () => {
  it("requires street, city and a two-letter country and bounds the optional parts", () => {
    const empty = { countryCode: "", regionCode: "", city: "", postalCode: "", street1: "", street2: "", purpose: "business" };
    expect(Object.keys(validateAddress(empty))).toEqual(["countryCode", "city", "street1"]);
    expect(Object.keys(validateAddress({ ...empty, countryCode: "USA", city: "x", street1: "y", regionCode: "MICH", postalCode: "1".repeat(13) }))).toEqual([
      "countryCode",
      "regionCode",
      "postalCode",
    ]);
    expect(validateAddress({ ...empty, countryCode: "us", city: "Detroit", street1: "1 Main St" })).toEqual({});
  });
});

describe("address forms", () => {
  it("shows no Address button without org.address.write", async () => {
    serve({ ...entities, "GET /sites": () => json({ items: [site] }) });
    renderPage("/org/sites", SitesPage, ["org.read", "org.site.write"]);
    const table = await findTable({ name: "Sites" });
    expect(within(table).getByRole("button", { name: "Edit site DET" })).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: /address of site DET/ })).toBeNull();
  });

  it("creates the address with the owner id, then points the site at it and shows the row from the response", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: [site] }),
      "POST /addresses": () => json(address, 201),
      "PATCH /sites/s-1": () => json({ ...site, addressId: "a-1", version: 4 }),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_ADDRESS);
    const { table, form } = await openAddress(user, "Add address of site DET");
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    expect(await within(form).findByText("Enter 1 to 100 characters.")).toBeInTheDocument();
    expect(callsTo(fetchMock, "POST", "/addresses")).toHaveLength(0);
    await fill(user, form);
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/sites/s-1")).toHaveLength(1));
    const post = callsTo(fetchMock, "POST", "/addresses")[0] as [unknown, RequestInit | undefined];
    expect(bodyOf(post)).toEqual({
      ownerType: "site",
      ownerId: "s-1",
      countryCode: "US",
      regionCode: "MI",
      city: "Detroit",
      street1: "1 Main St",
      purposes: [{ purpose: "delivery", primary: true }],
    });
    expect(headerOf(post, "X-Passage-LegalEntity")).toBe("le-1");
    expect(headerOf(post, "Idempotency-Key")).toBeTruthy();
    const patch = callsTo(fetchMock, "PATCH", "/sites/s-1")[0] as [unknown, RequestInit | undefined];
    expect(bodyOf(patch)).toEqual({ version: 3, addressId: "a-1" });
    expect(headerOf(patch, "X-Passage-LegalEntity")).toBe("le-1");
    await waitFor(() => expect(screen.queryByRole("form", { name: "Edit address of site DET" })).toBeNull());
    expect(within(table).getByRole("button", { name: "Edit address of site DET" })).toBeInTheDocument();
  });

  it("after a version conflict on the pointer retries only the pointer with the new version and never creates a second address", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...entities,
      "GET /sites/s-1": () => json({ ...site, name: "Detroit plant", version: 6 }),
      "GET /sites": () => json({ items: [site] }),
      "POST /addresses": () => json(posted, 201),
      "PATCH /sites/s-1": () =>
        ++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "current version is 6") : json({ ...site, addressId: "a-1", version: 7 }),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_ADDRESS);
    const { table, form } = await openAddress(user, "Add address of site DET");
    await fill(user, form);
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    expect(await within(form).findByText(/current version is 6/)).toBeInTheDocument();
    expect(await within(table).findByText("Detroit plant")).toBeInTheDocument();
    expect(within(form).getByLabelText("Street")).toHaveValue("1 Main St");
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/sites/s-1")).toHaveLength(2));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/sites/s-1")[1] as never)).toEqual({ version: 6, addressId: "a-1" });
    expect(callsTo(fetchMock, "POST", "/addresses")).toHaveLength(1);
  });

  it("revises a field changed after a failed pointer step before linking the address, still without a second address", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...entities,
      "GET /sites/s-1": () => json({ ...site, version: 6 }),
      "GET /sites": () => json({ items: [site] }),
      "POST /addresses": () => json(posted, 201),
      "PATCH /addresses/a-1": () => json({ ...posted, versionId: "v-2", street1: "2 Main St" }),
      "PATCH /sites/s-1": () => (++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "current version is 6") : json({ ...site, addressId: "a-1", version: 7 })),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_ADDRESS);
    const { table, form } = await openAddress(user, "Add address of site DET");
    await fill(user, form);
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    expect(await within(form).findByText(/ORG-VERSION-CONFLICT/)).toBeInTheDocument();
    await user.clear(within(form).getByLabelText("Street"));
    await user.type(within(form).getByLabelText("Street"), "2 Main St");
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/sites/s-1")).toHaveLength(2));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/addresses/a-1")[0] as never)).toEqual({ versionId: "v-1", street1: "2 Main St" });
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/sites/s-1")[1] as never)).toEqual({ version: 6, addressId: "a-1" });
    expect(callsTo(fetchMock, "POST", "/addresses")).toHaveLength(1);
    expect(await within(table).findByRole("button", { name: "Edit address of site DET" })).toBeInTheDocument();
  });

  it("links an address the owner already has but does not point at (for example after a reload) instead of creating another", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: [site] }),
      "GET /addresses?": () => json({ items: [posted] }),
      "PATCH /sites/s-1": () => json({ ...site, addressId: "a-1", version: 4 }),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_ADDRESS);
    const { form } = await openAddress(user, "Add address of site DET");
    const lookup = new URL(String(callsTo(fetchMock, "GET", "/addresses?")[0][0]), "http://x");
    expect(Object.fromEntries(lookup.searchParams)).toEqual({ ownerType: "site", ownerId: "s-1", limit: "100" });
    expect(headerOf(callsTo(fetchMock, "GET", "/addresses?")[0] as never, "X-Passage-LegalEntity")).toBe("le-1");
    expect(within(form).getByText(/not linked to it\. Saving links it/)).toBeInTheDocument();
    expect(within(form).getByLabelText("Street")).toHaveValue("1 Main St");
    expect(within(form).queryByLabelText("Purpose")).toBeNull();
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/sites/s-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/sites/s-1")[0] as never)).toEqual({ version: 3, addressId: "a-1" });
    expect(callsTo(fetchMock, "POST", "/addresses")).toHaveLength(0);
    expect(callsTo(fetchMock, "PATCH", "/addresses")).toHaveLength(0);
  });

  it("shows the service's country-rule refusal on the field it concerns", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: [site] }),
      "POST /addresses": () => problem("ORG-ADDR-REGION-UNKNOWN", 422, "region is unknown or inactive for the country"),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_ADDRESS);
    const { form } = await openAddress(user, "Add address of site DET");
    await fill(user, form);
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    expect(await within(form).findByText("This region is unknown or inactive for the country.")).toBeInTheDocument();
    expect(within(form).getByLabelText(/Region/)).toHaveAttribute("aria-invalid", "true");
    expect(callsTo(fetchMock, "PATCH", "/sites")).toHaveLength(0);
  });

  it("loads the current address, sends only the changed fields with its version and clears an optional field with an empty string", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: [{ ...site, addressId: "a-1" }] }),
      "GET /addresses/a-1": () => json(address),
      "PATCH /addresses/a-1": () => json({ ...address, versionId: "v-2", regionCode: undefined, city: "Ann Arbor" }),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_ADDRESS);
    const { form } = await openAddress(user, "Edit address of site DET");
    expect(within(form).getByLabelText("City")).toHaveValue("Detroit");
    expect(within(form).queryByLabelText("Purpose")).toBeNull();
    await user.clear(within(form).getByLabelText("City"));
    await user.type(within(form).getByLabelText("City"), "Ann Arbor");
    await user.clear(within(form).getByLabelText(/Region/));
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/addresses/a-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/addresses/a-1")[0] as never)).toEqual({ versionId: "v-1", city: "Ann Arbor", regionCode: "" });
    expect(callsTo(fetchMock, "PATCH", "/sites")).toHaveLength(0);
    await waitFor(() => expect(screen.queryByRole("form", { name: "Edit address of site DET" })).toBeNull());
  });

  it("re-reads a stale address on conflict and keeps the user's edit", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: [{ ...site, addressId: "a-1" }] }),
      "GET /addresses/a-1": () => json(patches === 0 ? address : { ...address, versionId: "v-2", street1: "9 Elm St" }),
      "PATCH /addresses/a-1": () =>
        ++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "current version is v-2") : json({ ...address, versionId: "v-3", city: "Ann Arbor", street1: "9 Elm St" }),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_ADDRESS);
    const { form } = await openAddress(user, "Edit address of site DET");
    await user.clear(within(form).getByLabelText("City"));
    await user.type(within(form).getByLabelText("City"), "Ann Arbor");
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    expect(await within(form).findByText(/your changes are kept/)).toBeInTheDocument();
    await waitFor(() => expect(within(form).getByLabelText("Street")).toHaveValue("9 Elm St"));
    expect(within(form).getByLabelText("City")).toHaveValue("Ann Arbor");
    await user.click(within(form).getByRole("button", { name: "Save address of site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/addresses/a-1")).toHaveLength(2));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/addresses/a-1")[1] as never)).toEqual({ versionId: "v-2", city: "Ann Arbor" });
  });

  it("sets a legal entity's primary address through primaryAddressId", async () => {
    const fetchMock = serve({
      ...entities,
      "POST /addresses": () => json({ ...address, ownerType: "legal_entity", ownerId: "le-1" }, 201),
      "PATCH /legal-entities/le-1": () => json({ ...entity, primaryAddressId: "a-1", version: 3 }),
    });
    const { user } = renderPage("/org/legal-entities", LegalEntitiesPage, ["org.read", "org.legal-entity.write", "org.address.write"]);
    const table = await findTable({ name: "Legal entities" });
    await user.click(within(table).getByRole("button", { name: "Add address of legal entity USMF" }));
    const form = await screen.findByRole("form", { name: "Edit address of legal entity USMF" });
    await user.type(within(form).getByLabelText("Street"), "1 Main St");
    await user.type(within(form).getByLabelText("City"), "Detroit");
    await user.type(within(form).getByLabelText("Country"), "us");
    await user.click(within(form).getByRole("button", { name: "Save address of legal entity USMF" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/legal-entities/le-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "POST", "/addresses")[0] as never)).toMatchObject({ ownerType: "legal_entity", ownerId: "le-1", purposes: [{ purpose: "business", primary: true }] });
    expect(headerOf(callsTo(fetchMock, "POST", "/addresses")[0] as never, "X-Passage-LegalEntity")).toBe("le-1");
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/legal-entities/le-1")[0] as never)).toEqual({ version: 2, primaryAddressId: "a-1" });
    expect(await within(table).findByRole("button", { name: "Edit address of legal entity USMF" })).toBeInTheDocument();
  });
});

describe("rows are replaced from the response, not merged", () => {
  it("drops a team's cleared end date after a version conflict re-read", async () => {
    const team = { id: "t-1", code: "MRP", name: "MRP planners", teamTypeId: "tt-1", effectiveFrom: "2026-01-01", effectiveTo: "2026-12-31", status: "active", version: 4 };
    serve({
      ...entities,
      "GET /team-types": () => json({ items: [{ id: "tt-1", code: "PLANNERS", name: "Planners", memberKinds: ["user"] }] }),
      "GET /teams/t-1": () => {
        const { effectiveTo: _gone, ...open } = team;
        return json({ ...open, version: 6 });
      },
      "GET /teams": () => json({ items: [team] }),
      "PATCH /teams/t-1": () => problem("ORG-VERSION-CONFLICT", 409, "current version is 6"),
    });
    const { user } = renderPage("/org/teams", TeamsPage, ["org.read", "org.operating-unit.write", "tenant.administer"]);
    const table = await findTable({ name: "Teams" });
    expect(within(table).getByText("2026-12-31")).toBeInTheDocument();
    await user.click(within(table).getByRole("button", { name: "Deactivate team MRP" }));
    await user.click(within(table).getByRole("button", { name: "Confirm deactivate team MRP" }));
    await waitFor(() => expect(within(table).queryByText("2026-12-31")).toBeNull());
  });
});
