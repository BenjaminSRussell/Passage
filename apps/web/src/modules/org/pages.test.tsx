import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import org from "./module";
import { stubLocalStorage } from "./testStorage";

const entities = [
  { id: "le-1", code: "LE1", name: "Alpha", country: "US", currency: "USD", timeZone: "UTC", status: "active", version: 3 },
  { id: "le-2", code: "LE2", name: "Beta", country: "DE", currency: "EUR", timeZone: "UTC", status: "inactive", version: 1 },
];

function renderAt(path: string) {
  return render(<RouterProvider router={createMemoryRouter(org.routes, { initialEntries: [path] })} />);
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

describe("Legal entities page", () => {
  it("lists entities with status pills", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ items: entities })));
    renderAt("/org/legal-entities");
    const table = await findTable({ name: "Legal entities" });
    await waitFor(() => expect(within(table).getByText("Alpha")).toBeInTheDocument());
    expect(within(table).getByText("Active")).toBeInTheDocument();
    expect(within(table).getByText("Inactive")).toBeInTheDocument();
    expect(within(table).getByText("EUR")).toBeInTheDocument();
  });

  it("shows a loading skeleton while the request is pending", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => undefined)));
    renderAt("/org/legal-entities");
    expect(await screen.findByRole("table", { name: "Legal entities" })).toHaveAttribute("aria-busy", "true");
  });

  it("explains an empty list", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ items: [] })));
    renderAt("/org/legal-entities");
    expect(await screen.findByRole("heading", { name: "No legal entities" })).toBeInTheDocument();
  });

  it("shows the problem code on error and disables the switcher", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ type: "t", title: "Boom", status: 500, code: "ORG-X-UNKNOWN", detail: "it broke" }, 500)),
    );
    renderAt("/org/legal-entities");
    expect((await screen.findAllByText(/ORG-X-UNKNOWN/)).length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Legal entity")).toBeDisabled();
  });
});

describe("scoped list pages", () => {
  it("shows the active scope in the header and the sites of that entity", async () => {
    const fetchMock = vi.fn(async (url: string) =>
      url.includes("/sites")
        ? json({ items: [{ id: "s1", legalEntityId: "le-1", code: "SITE-1", name: "HQ", timeZone: "UTC", status: "active", version: 1 }] })
        : json({ items: entities }),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderAt("/org/sites");
    expect(await screen.findByText("SITE-1")).toBeInTheDocument();
    expect(screen.getByText("Scope: LE1")).toBeInTheDocument();
    expect(screen.queryByText("Viewing all entities")).not.toBeInTheDocument();
  });
});
