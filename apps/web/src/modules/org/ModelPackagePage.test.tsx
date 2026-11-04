import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ModelPackagePage } from "./ModelPackagePage";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

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

function renderPage(duties: string[] = ["org.read", "org.package.manage", "tenant.administer"]) {
  const router = createMemoryRouter(
    [{ element: createElement(OrgLayout, { duties }), children: [{ path: "/org/package", element: createElement(ModelPackagePage) }] }],
    { initialEntries: ["/org/package"] },
  );
  return { user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}

const packageText = JSON.stringify({ schemaVersion: 1, legalEntities: [{ code: "USMF" }] });
const report = {
  dryRun: true,
  created: 3,
  unchanged: 1,
  kinds: { legalEntities: { created: 1, unchanged: 1 }, sites: { created: 2, unchanged: 0 } },
  differences: [{ kind: "legalEntities", code: "USMF", field: "name" }],
};

const callsTo = (fetchMock: ReturnType<typeof serve>, path: string) => fetchMock.mock.calls.filter(([url]) => String(url).endsWith(path));

async function paste(user: ReturnType<typeof userEvent.setup>, text: string) {
  const box = await screen.findByLabelText("Package JSON");
  await user.click(box);
  await user.paste(text);
}

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

describe("model package page", () => {
  it("shows an empty state before any run and keeps import disabled without text", async () => {
    serve({});
    renderPage();
    expect(await screen.findByText("No import run yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import package" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Check package" })).toBeDisabled();
  });

  it("is read only without the package duty or tenant administration", async () => {
    for (const duties of [["org.read", "tenant.administer"], ["org.read", "org.package.manage"]]) {
      const fetchMock = serve({});
      const { unmount } = renderPage(duties);
      expect(await screen.findByText("Read only")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Export package" })).toBeNull();
      expect(callsTo(fetchMock, "/import")).toHaveLength(0);
      unmount();
    }
  });

  it("checks a package with a dry run and lists counts and differences", async () => {
    const fetchMock = serve({ "POST /import?dryRun=true": () => json(report) });
    const { user } = renderPage();
    await paste(user, packageText);
    await user.click(screen.getByRole("button", { name: "Check package" }));
    expect(await screen.findByText(/Nothing was written/)).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Records by kind" });
    expect(within(table).getByText("Sites")).toBeInTheDocument();
    expect(screen.getByText(/left as they are/)).toBeInTheDocument();
    const [, init] = callsTo(fetchMock, "/import?dryRun=true")[0];
    expect(init?.body).toBe(packageText);
  });

  it("labels versions and calendars and lists what the import left out", async () => {
    serve({
      "POST /import?dryRun=true": () =>
        json({
          ...report,
          kinds: { hierarchyVersions: { created: 2, unchanged: 0 }, fiscalCalendars: { created: 1, unchanged: 0 }, fiscalYears: { created: 3, unchanged: 0 } },
          differences: [{ kind: "hierarchyVersions", code: "REPORTING/2026-01-01", field: "nodes" }],
          skipped: [{ kind: "hierarchyVersions", code: "SECURITY/2026-01-01", reason: "publish this version in the designer" }],
        }),
    });
    const { user } = renderPage();
    await paste(user, packageText);
    await user.click(screen.getByRole("button", { name: "Check package" }));
    const table = await screen.findByRole("table", { name: "Records by kind" });
    for (const label of ["Hierarchy versions", "Fiscal calendars", "Fiscal years"]) expect(within(table).getByText(label)).toBeInTheDocument();
    expect(screen.getByText("Left out on purpose")).toBeInTheDocument();
    expect(screen.getByText(/SECURITY\/2026-01-01: publish this version in the designer/)).toBeInTheDocument();
    expect(screen.getByText(/REPORTING\/2026-01-01: nodes/)).toBeInTheDocument();
  });

  it("imports for real without the dry-run flag", async () => {
    const fetchMock = serve({ "POST /import": () => json({ ...report, dryRun: false }) });
    const { user } = renderPage();
    await paste(user, packageText);
    await user.click(screen.getByRole("button", { name: "Import package" }));
    expect(await screen.findByText("Created 3 records; 1 were already present.")).toBeInTheDocument();
    expect(callsTo(fetchMock, "/import")).toHaveLength(1);
  });

  it("shows each invalid path of a rejected package", async () => {
    serve({
      "POST /import": () =>
        json({ code: "ORG-PKG-INVALID", detail: "the package failed validation; nothing was imported", errors: { "sites[0].legalEntity": "unknown legal entity NOPE" } }, 422),
    });
    const { user } = renderPage();
    await paste(user, packageText);
    await user.click(screen.getByRole("button", { name: "Import package" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("ORG-PKG-INVALID");
    expect(alert).toHaveTextContent("sites[0].legalEntity");
    expect(alert).toHaveTextContent("unknown legal entity NOPE");
  });

  it("refuses text that is not JSON without calling the service", async () => {
    const fetchMock = serve({});
    const { user } = renderPage();
    await paste(user, "not json");
    await user.click(screen.getByRole("button", { name: "Check package" }));
    expect(await screen.findByText("The text is not valid JSON.", { exact: false })).toBeInTheDocument();
    expect(callsTo(fetchMock, "/import")).toHaveLength(0);
  });

  it("downloads the export as org-package.json", async () => {
    serve({ "POST /export": () => json({ schemaVersion: 1, exportedAt: "2026-11-04T10:00:00Z", legalEntities: [] }) });
    const create = vi.fn(() => "blob:x");
    const revoke = vi.fn();
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const { user } = renderPage();
    await user.click(await screen.findByRole("button", { name: "Export package" }));
    await waitFor(() => expect(click).toHaveBeenCalled());
    expect(create).toHaveBeenCalled();
    expect(await screen.findByText(/Exported 2026-11-04T10:00:00Z/)).toBeInTheDocument();
    click.mockRestore();
  });

  it("shows an export failure", async () => {
    serve({ "POST /export": () => json({ code: "AUTH-0003", detail: "the tenant.administer duty is required" }, 403) });
    const { user } = renderPage();
    await user.click(await screen.findByRole("button", { name: "Export package" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("AUTH-0003");
  });
});
