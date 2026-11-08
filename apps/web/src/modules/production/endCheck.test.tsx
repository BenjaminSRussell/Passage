import { findAlert } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { getEndCheck } from "./api/client";
import production from "./module";
import { json, order, orderId, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const urls = (mock: ReturnType<typeof vi.fn>) => (mock.mock.calls as unknown as Array<[string]>).map(([url]) => url);

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
}

function detailFetch(current: ReturnType<typeof order>, endCheck: Response | (() => Response)) {
  return stubFetch((url) => {
    if (url.endsWith("/end-check")) {
      return endCheck instanceof Response ? endCheck : endCheck();
    }
    if (url.endsWith("/status-log") || url.endsWith("/journals") || url.endsWith("/as-built") || url.endsWith("/trigger-points") || url.endsWith("/jobs")) {
      return json({ orderId, items: [] });
    }
    if (url.includes("reworkOf=") || url.includes("/bom-edits")) {
      return json({ orderId, items: [] });
    }
    if (url.endsWith("/costs")) {
      return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
    }
    return json(current);
  });
}

describe("end check client", () => {
  it("reads the check and fills what the body leaves out", async () => {
    const fetchMock = stubFetch(() => json({ canEnd: true }));
    expect(await getEndCheck(orderId)).toEqual({ orderId, canEnd: true, blockers: [], warnings: [] });
    expect(urls(fetchMock)[0]).toBe(`/api/v1/production/orders/${orderId}/end-check`);
  });
});

describe("Ready to end panel on the order page", () => {
  it("lists the blockers and the warnings of a Reported as finished order", async () => {
    detailFetch(
      order({ status: "ReportedAsFinished", version: 7 }),
      json({
        orderId,
        canEnd: false,
        blockers: [{ code: "PRODUCTION_UNPOSTED_JOURNALS", message: "cannot end: 2 journals are unposted; post or delete them", count: 2 }],
        warnings: [{ code: "PRODUCTION_AS_BUILT_UNRESOLVED", message: "1 component serials are not assigned to a finished serial", count: 1 }],
      }),
    );
    const { container } = renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Cannot end yet")).toBeInTheDocument();
    expect(screen.getByText("cannot end: 2 journals are unposted; post or delete them")).toBeInTheDocument();
    expect(screen.getByText("1 component serials are not assigned to a finished serial")).toBeInTheDocument();
    expect(await checkAxeAA(container)).toEqual([]);
  });

  it("says when nothing stands in the way and reads again on request", async () => {
    let answers = 0;
    const fetchMock = detailFetch(order({ status: "Started", version: 3 }), () => {
      answers += 1;
      return json({ orderId, canEnd: true, blockers: [], warnings: [] });
    });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Can end")).toBeInTheDocument();
    expect(screen.getByText("Nothing stands in the way of ending this order.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Check again" }));
    await waitFor(() => expect(answers).toBe(2));
    expect(urls(fetchMock).filter((u) => u.endsWith("/end-check"))).toHaveLength(2);
  });

  it("shows the service's refusal when the check cannot be read", async () => {
    detailFetch(order({ status: "Started", version: 3 }), problem(403, "FORBIDDEN", "You may not read this order."));
    renderAt(`/production/${orderId}`);
    const alert = await findAlert();
    expect(within(alert).getByText(/The end check could not be read/)).toBeInTheDocument();
    expect(within(alert).getByText(/You may not read this order\./)).toBeInTheDocument();
  });

  it("is not offered before the order starts", async () => {
    const fetchMock = detailFetch(order({ status: "Released", version: 3 }), json({}));
    renderAt(`/production/${orderId}`);
    await screen.findByText("Status actions");
    expect(screen.queryByText("Ready to end?")).not.toBeInTheDocument();
    expect(urls(fetchMock).some((u) => u.endsWith("/end-check"))).toBe(false);
  });
});
