import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OperatingUnitImportPage } from "./OperatingUnitImportPage";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });

function serve(importReply: (url: string, init?: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) =>
    url.includes("operating-units:import")
      ? importReply(url, init)
      : json({ items: [] }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPage(
  duties: string[] = ["org.read", "org.operating-unit.write"],
) {
  const router = createMemoryRouter(
    [
      {
        element: createElement(OrgLayout, { duties }),
        children: [
          {
            path: "/org/operating-units/import",
            element: createElement(OperatingUnitImportPage),
          },
        ],
      },
    ],
    { initialEntries: ["/org/operating-units/import"] },
  );
  return {
    user: userEvent.setup(),
    ...render(<RouterProvider router={router} />),
  };
}

const csv =
  "type,code,name,legalEntity\ndepartment,D200,Assembly,USMF\ncost_center,CC1,Plant,";
const importCalls = (fetchMock: ReturnType<typeof serve>) =>
  fetchMock.mock.calls.filter(([url]) =>
    String(url).includes("operating-units:import"),
  );

async function paste(user: ReturnType<typeof userEvent.setup>, text: string) {
  await user.click(await screen.findByLabelText("CSV text"));
  await user.paste(text);
}

const checkReport = {
  dryRun: true,
  applied: false,
  created: 1,
  unchanged: 1,
  failed: 0,
  rows: [
    { line: 1, type: "department", code: "D200", status: "created" },
    { line: 2, type: "cost_center", code: "CC1", status: "unchanged", id: "x" },
  ],
};

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

describe("operating unit import page", () => {
  it("shows an empty state and keeps both actions disabled without text", async () => {
    serve(() => json({}));
    renderPage();
    expect(await screen.findByText("No import run yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check file" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Import operating units" }),
    ).toBeDisabled();
  });

  it("is read only without the write duty", async () => {
    const fetchMock = serve(() => json({}));
    renderPage(["org.read"]);
    expect(await screen.findByText("Read only")).toBeInTheDocument();
    expect(screen.queryByLabelText("CSV text")).not.toBeInTheDocument();
    expect(importCalls(fetchMock)).toHaveLength(0);
  });

  it("checks the file with dryRun and lists each row", async () => {
    const fetchMock = serve(() => json(checkReport));
    const { user } = renderPage();
    await paste(user, csv);
    expect(await screen.findByText("2 rows read.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Check file" }));
    expect(
      await screen.findByText(
        /An import would create 1 operating units and find 1 already present/,
      ),
    ).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Import rows" });
    expect(within(table).getByText("To create")).toBeInTheDocument();
    expect(within(table).getByText("Already present")).toBeInTheDocument();
    const [url, init] = importCalls(fetchMock)[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/api/v1/org/operating-units:import?dryRun=true");
    expect(JSON.parse(String(init.body)).rows).toHaveLength(2);
  });

  it("imports and reports what was created", async () => {
    const fetchMock = serve(() =>
      json({ ...checkReport, dryRun: false, applied: true }),
    );
    const { user } = renderPage();
    await paste(user, csv);
    await user.click(
      screen.getByRole("button", { name: "Import operating units" }),
    );
    expect(
      await screen.findByText(
        "Created 1 operating units; 1 were already present.",
      ),
    ).toBeInTheDocument();
    expect(importCalls(fetchMock)[0][0]).toBe(
      "/api/v1/org/operating-units:import",
    );
  });

  it("lists failed rows with their problems and says nothing was written", async () => {
    serve(() =>
      json({
        dryRun: false,
        applied: false,
        created: 0,
        unchanged: 0,
        failed: 1,
        rows: [
          {
            line: 1,
            type: "department",
            code: "D200",
            status: "failed",
            errors: {
              legalEntity:
                "is not an active legal entity visible to the caller",
            },
          },
        ],
      }),
    );
    const { user } = renderPage();
    await paste(user, csv);
    await user.click(
      screen.getByRole("button", { name: "Import operating units" }),
    );
    expect(
      await screen.findByText(/1 rows failed, so nothing was written/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/legalEntity: is not an active legal entity/),
    ).toBeInTheDocument();
  });

  it("blocks a file with no usable header before any request", async () => {
    const fetchMock = serve(() => json({}));
    const { user } = renderPage();
    await paste(user, "code,title\nA,B");
    expect(
      await screen.findByText("The header has no type column."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check file" })).toBeDisabled();
    expect(importCalls(fetchMock)).toHaveLength(0);
  });

  it("shows the service's problem when the request fails", async () => {
    serve(() =>
      json(
        {
          code: "ORG-OU-IMPORT-TOO-LARGE",
          detail: "an import takes at most 1000 rows",
        },
        422,
      ),
    );
    const { user } = renderPage();
    await paste(user, csv);
    await user.click(screen.getByRole("button", { name: "Check file" }));
    expect(
      await screen.findByText(
        /an import takes at most 1000 rows \(ORG-OU-IMPORT-TOO-LARGE\)/,
      ),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Check file" })).toBeEnabled(),
    );
  });
});
