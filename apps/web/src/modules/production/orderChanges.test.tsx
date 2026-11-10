import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { listOCMProfiles, listOrderChanges, saveOCMProfile, type ChangePreview, type OCMProfile, type OrderChange } from "./api/client";
import production from "./module";
import { describeDelta } from "./labels";
import { validateOCMProfile, blankOCMProfile } from "./OcmProfilesPage";
import { json, order, orderId, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

function renderAt(path: string) {
  const at = { pathname: path };
  function Probe() {
    at.pathname = useLocation().pathname;
    return null;
  }
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Probe />
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
  return { location: at, ...view };
}

const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;
const changeId = uuid(500);
const noDelta = { linesAdded: 0, linesRemoved: 0, linesChanged: 0, operationsAdded: 0, operationsRemoved: 0, operationsChanged: 0 };

const change = (overrides: Partial<OrderChange> = {}): OrderChange => ({
  id: changeId,
  orderId,
  orderNumber: "PO-000001",
  productId: uuid(2),
  siteId: uuid(3),
  ecoId: "ECO-1042",
  status: "pending",
  profileMode: "manual",
  needsReview: false,
  changesBom: true,
  changesRoute: false,
  delta: { ...noDelta, linesAdded: 1, linesRemoved: 1, linesChanged: 1 },
  deltaCount: 3,
  currency: "USD",
  changeCost: "575.00",
  beforeHash: null,
  afterHash: null,
  note: "",
  version: 1,
  detectedBy: "engineer-1",
  detectedAt: "2026-10-30T09:00:00Z",
  resolvedBy: null,
  resolvedAt: null,
  ...overrides,
});

const preview = (overrides: Partial<ChangePreview> = {}): ChangePreview => ({
  change: change(),
  delta: change().delta,
  cost: { oldPlanned: "5000.00", newPlanned: "5500.00", delta: "500.00", surcharge: "75.00", total: "575.00" },
  blocked: null,
  ...overrides,
});

describe("order change client", () => {
  it("sends only the filters given and tolerates a body without a list", async () => {
    const fetchMock = stubFetch(() => json({}));
    const page = await listOrderChanges({ status: "pending", needsReview: true, ecoId: "ECO-1", orderId });
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/order-changes?status=pending&needsReview=true&ecoId=ECO-1&orderId=${orderId}&limit=50`);
    expect(page).toEqual({ items: [], nextCursor: undefined });
    await listOrderChanges();
    expect(calls(fetchMock)[1][0]).toBe("/api/v1/production/order-changes?limit=50");
  });

  it("lists and saves OCM profiles, replacing under If-Match", async () => {
    const profile: OCMProfile = { itemGroup: "FG", siteId: "All", mode: "automatic", surchargeFixed: "25", surchargePct: "10", version: 2 };
    const fetchMock = stubFetch((_url, init) => (init?.method === "PUT" ? json(profile) : json({ items: [profile] })));
    expect(await listOCMProfiles()).toEqual([profile]);
    await saveOCMProfile(blankOCMProfile());
    await saveOCMProfile(blankOCMProfile(), 2);
    const [, create] = calls(fetchMock)[1];
    const [, replace] = calls(fetchMock)[2];
    expect((create?.headers as Record<string, string>)["If-Match"]).toBeUndefined();
    expect((replace?.headers as Record<string, string>)["If-Match"]).toBe('"2"');
  });

  it("describes a delta in words", () => {
    expect(describeDelta(noDelta)).toBe("No difference");
    expect(describeDelta({ ...noDelta, linesAdded: 2, operationsChanged: 1 })).toBe("2 lines added, 1 operations changed");
  });
});

describe("order changes worklist (4.21.14, FR-21.16)", () => {
  it("lists pending changes with delta and cost, filters, loads more and opens a change", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("cursor=")) {
        return json({ items: [change({ id: uuid(501), orderNumber: "PO-000009", ecoId: "ECO-7" })] });
      }
      if (url.includes("/order-changes?")) {
        return json({
          items: [change(), change({ id: uuid(502), orderNumber: "PO-000002", needsReview: true, profileMode: "automatic", changeCost: "-40.00" })],
          nextCursor: "cursor-1",
        });
      }
      return url.endsWith(":preview") ? json(preview()) : json(change());
    });
    const { container, location } = renderAt("/production/order-changes");
    const table = await findTable({ name: "Order changes" });
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/order-changes?status=pending&limit=50");
    expect(within(table).getByText("PO-000002")).toBeInTheDocument();
    expect(within(table).getByText("575.00 USD")).toBeInTheDocument();
    expect(within(table).getByText("-40.00 USD")).toBeInTheDocument();
    expect(within(table).getByText("Edited by hand")).toBeInTheDocument();
    expect(within(table).getAllByText("1 lines added, 1 lines removed, 1 lines changed")).toHaveLength(2);
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(await screen.findByRole("button", { name: "Load more changes" }));
    expect(await screen.findByText("PO-000009")).toBeInTheDocument();
    expect(calls(fetchMock).at(-1)?.[0]).toContain("cursor=cursor-1");

    const form = screen.getByRole("form", { name: "Filter the order changes" });
    await userEvent.selectOptions(within(form).getByLabelText("Status"), "applied");
    await userEvent.type(within(form).getByLabelText(/ECO/), "ECO-1042");
    await userEvent.selectOptions(within(form).getByLabelText("Orders"), "review");
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    await waitFor(() =>
      expect(calls(fetchMock).some(([url]) => url === "/api/v1/production/order-changes?status=applied&ecoId=ECO-1042&needsReview=true&limit=50")).toBe(true),
    );

    await userEvent.click(await screen.findByText("PO-000002"));
    expect(location.pathname).toBe(`/production/order-changes/${uuid(502)}`);
  });

  it("shows loading, empty, no-duty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt("/production/order-changes");
    expect(await screen.findByRole("table", { name: "Order changes" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    const second = renderAt("/production/order-changes");
    expect(await screen.findByRole("heading", { name: "No order changes" })).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    const third = renderAt("/production/order-changes");
    expect(await screen.findByText("You need the View production orders duty to see the order changes.")).toBeInTheDocument();
    third.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "database unavailable"));
    renderAt("/production/order-changes");
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
  });
});

describe("order change detail (FR-21.16, BR-21.12)", () => {
  const path = `/production/order-changes/${changeId}`;

  it("previews the change cost and applies it with the change's version and one idempotency key", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (url.endsWith(":preview")) return json(preview());
      if (url.endsWith(":apply")) {
        return json({ change: change({ status: "applied", version: 2, resolvedBy: "planner-1", resolvedAt: "2026-10-30T10:00:00Z", beforeHash: "aaaaaaaaaaaabbbb", afterHash: "ccccccccccccdddd" }), order: order({ version: 5 }) });
      }
      expect(init?.method ?? "GET").toBe("GET");
      return json(change());
    });
    const { container } = renderAt(path);
    expect(await screen.findByRole("heading", { name: "Order PO-000001: ECO-1042" })).toBeInTheDocument();
    const stats = screen.getByRole("list", { name: "Change cost in USD" });
    expect(stats).toHaveTextContent("Planned now 5000.00");
    expect(stats).toHaveTextContent("Surcharge 75.00");
    expect(stats).toHaveTextContent("Change cost 575.00 USD");
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(screen.getByRole("button", { name: "Apply change" }));
    expect(await screen.findByText("Change applied: order PO-000001 is now at version 5 with its planned cost re-estimated.")).toBeInTheDocument();
    const apply = calls(fetchMock).find(([url]) => url.endsWith(":apply"));
    const headers = apply?.[1]?.headers as Record<string, string>;
    expect(headers["If-Match"]).toBe('"1"');
    expect(headers["Idempotency-Key"]).toMatch(/\S+/);
    expect(await screen.findByText("aaaaaaaaaaaa")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply change" })).not.toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Dismiss the change" })).not.toBeInTheDocument();
  });

  it("keeps the idempotency key when an apply fails, so a retry replays", async () => {
    let attempts = 0;
    const fetchMock = stubFetch((url) => {
      if (url.endsWith(":preview")) return json(preview());
      if (url.endsWith(":apply")) {
        attempts += 1;
        return attempts === 1 ? problem(500, "COM-0500", "database unavailable") : json({ change: change({ status: "applied", version: 2 }), order: order({ version: 3 }) });
      }
      return json(change());
    });
    renderAt(path);
    await userEvent.click(await screen.findByRole("button", { name: "Apply change" }));
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Apply change" }));
    await screen.findByText(/Change applied/);
    const keys = calls(fetchMock)
      .filter(([url]) => url.endsWith(":apply"))
      .map(([, init]) => (init?.headers as Record<string, string>)["Idempotency-Key"]);
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);
  });

  it("explains a blocked change and disables apply", async () => {
    stubFetch((url) =>
      url.endsWith(":preview")
        ? json(preview({ blocked: { code: "PRODUCTION_ORDER_EDIT_TARGET_IN_USE", detail: "line 20.1 has consumption and cannot be removed" } }))
        : json(change({ needsReview: true })),
    );
    renderAt(path);
    expect(await screen.findByText("line 20.1 has consumption and cannot be removed (PRODUCTION_ORDER_EDIT_TARGET_IN_USE)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply change" })).toBeDisabled();
    expect(screen.getByText("Edited by hand: review before applying")).toBeInTheDocument();
  });

  it("dismisses with a note and refuses a note that is too long", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.endsWith(":preview")) return json(preview());
      if (url.endsWith(":dismiss")) return json(change({ status: "dismissed", version: 2, note: "keep v2", resolvedBy: "planner-1", resolvedAt: "2026-10-30T10:00:00Z" }));
      return json(change());
    });
    renderAt(path);
    const form = await screen.findByRole("form", { name: "Dismiss the change" });
    await userEvent.type(within(form).getByLabelText(/Note/), "x".repeat(501), { delay: null });
    await userEvent.click(within(form).getByRole("button", { name: "Dismiss change" }));
    expect(await screen.findByText("Keep the note to 500 characters or fewer.")).toBeInTheDocument();
    expect(calls(fetchMock).some(([url]) => url.endsWith(":dismiss"))).toBe(false);

    await userEvent.clear(within(form).getByLabelText(/Note/));
    await userEvent.type(within(form).getByLabelText(/Note/), " keep v2 ");
    await userEvent.click(within(form).getByRole("button", { name: "Dismiss change" }));
    expect(await screen.findByText("Change dismissed: the order stays on its current BOM and route.")).toBeInTheDocument();
    const dismiss = calls(fetchMock).find(([url]) => url.endsWith(":dismiss"));
    expect(JSON.parse(String(dismiss?.[1]?.body))).toEqual({ note: "keep v2" });
    expect((dismiss?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(screen.getByText("Dismissed")).toBeInTheDocument();
  }, 60_000);

  it("shows a conflict from the service and the missing-change state", async () => {
    stubFetch((url) => {
      if (url.endsWith(":preview")) return json(preview());
      if (url.endsWith(":apply")) return problem(409, "PRODUCTION_ORDER_CHANGE_RESOLVED", "the change was already dismissed");
      return json(change());
    });
    const first = renderAt(path);
    await userEvent.click(await screen.findByRole("button", { name: "Apply change" }));
    expect(await screen.findByText("the change was already dismissed")).toBeInTheDocument();
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(404, "PRODUCTION_ORDER_CHANGE_NOT_FOUND", "order change not found"));
    const second = renderAt(path);
    expect(await screen.findByText("This order change does not exist.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Back to order changes" }));
    expect(second.location.pathname).toBe("/production/order-changes");
  });

  it("shows the record without preview or actions to a user without the apply duty", async () => {
    stubFetch((url) =>
      url.endsWith(":preview") ? problem(403, "AUTH-0003", "missing duty production.order.apply-change") : json(change({ note: "" })),
    );
    const { container } = renderAt(path);
    expect(await screen.findByText("You need the Apply order change duty to preview, apply or dismiss order changes.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Order PO-000001: ECO-1042" })).toBeInTheDocument();
    expect(screen.getByText("575.00 USD")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply change" })).not.toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Dismiss the change" })).not.toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("explains a change a later ECO superseded, and shows why an older one was dismissed", async () => {
    stubFetch((url) =>
      url.endsWith(":preview")
        ? json(preview({ blocked: { code: "PRODUCTION_ORDER_CHANGE_SUPERSEDED", detail: "the later ECO-2000 replaces the same BOM or route of this order" } }))
        : json(change()),
    );
    const first = renderAt(path);
    expect(await screen.findByText(/the later ECO-2000 replaces the same BOM or route/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply change" })).toBeDisabled();
    first.unmount();

    vi.unstubAllGlobals();
    const fetchMock = stubFetch(() =>
      json(change({ status: "dismissed", note: "superseded by ECO-2000", resolvedBy: "engineer-1", resolvedAt: "2026-10-30T11:00:00Z", version: 2 })),
    );
    renderAt(path);
    expect(await screen.findByText("superseded by ECO-2000")).toBeInTheDocument();
    expect(calls(fetchMock).some(([url]) => url.endsWith(":preview"))).toBe(false);
  });

  it("opens the production order", async () => {
    stubFetch((url) => (url.endsWith(":preview") ? json(preview()) : url.includes("/order-changes/") ? json(change()) : json(order())));
    const { location } = renderAt(path);
    await userEvent.click(await screen.findByRole("button", { name: "Open production order" }));
    expect(location.pathname).toBe(`/production/${orderId}`);
  });
});

describe("order change profiles (FR-21.16)", () => {
  const profiles: OCMProfile[] = [{ itemGroup: "All", siteId: "All", mode: "manual", surchargeFixed: "25", surchargePct: "10", version: 1 }];

  it("validates like the service", () => {
    expect(validateOCMProfile(blankOCMProfile())).toEqual({});
    const errors = validateOCMProfile({ itemGroup: "", siteId: "site", mode: "sometimes" as never, surchargeFixed: "-1", surchargePct: "1001" });
    expect(Object.keys(errors).sort()).toEqual(["itemGroup", "mode", "siteId", "surchargeFixed", "surchargePct"]);
  });

  it("lists, creates and replaces a profile under If-Match", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init?.method === "PUT" ? json({ ...profiles[0], itemGroup: "FG", version: JSON.parse(String(init.body)).itemGroup === "FG" && (init.headers as Record<string, string>)["If-Match"] ? 2 : 1 }) : json({ items: profiles }),
    );
    const { container } = renderAt("/production/ocm-profiles");
    const table = await findTable({ name: "Order change profiles" });
    expect(within(table).getByText("Manual (the planner confirms)")).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Order change profile" });
    await userEvent.clear(within(form).getByLabelText(/Item group/));
    await userEvent.type(within(form).getByLabelText(/Item group/), "FG");
    await userEvent.selectOptions(within(form).getByLabelText("Mode"), "none");
    await userEvent.click(within(form).getByRole("button", { name: "Save profile" }));
    expect(await screen.findByText("Order change profile FG × All saved (version 1).")).toBeInTheDocument();
    const [, create] = calls(fetchMock).find(([, init]) => init?.method === "PUT")!;
    expect(JSON.parse(String(create?.body))).toMatchObject({ itemGroup: "FG", mode: "none" });

    await userEvent.click(within(table).getByText("Manual (the planner confirms)"));
    await userEvent.click(within(form).getByRole("button", { name: "Save profile" }));
    await waitFor(() => expect(calls(fetchMock).filter(([, init]) => init?.method === "PUT")).toHaveLength(2));
  });

  it("refuses an invalid form and shows the service's refusal", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init?.method === "PUT" ? problem(412, "conflict_version", "the OCM profile was changed by another user") : json({ items: [] }),
    );
    renderAt("/production/ocm-profiles");
    expect(await screen.findByRole("heading", { name: "No profiles yet" })).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Order change profile" });
    await userEvent.clear(within(form).getByLabelText(/Fixed surcharge/));
    await userEvent.type(within(form).getByLabelText(/Fixed surcharge/), "abc");
    await userEvent.click(within(form).getByRole("button", { name: "Save profile" }));
    expect(await screen.findByText("Enter an amount of 0 or more, with up to 4 decimals.")).toBeInTheDocument();
    expect(calls(fetchMock).some(([, init]) => init?.method === "PUT")).toBe(false);

    await userEvent.clear(within(form).getByLabelText(/Fixed surcharge/));
    await userEvent.type(within(form).getByLabelText(/Fixed surcharge/), "5");
    await userEvent.click(within(form).getByRole("button", { name: "Save profile" }));
    expect(await screen.findByText("the OCM profile was changed by another user")).toBeInTheDocument();
  });

  it("shows loading and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt("/production/ocm-profiles");
    expect(await screen.findByRole("table", { name: "Order change profiles" })).toHaveAttribute("aria-busy", "true");
    first.unmount();
    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderAt("/production/ocm-profiles");
    expect(await screen.findByText("You need the View production setup duty to see the profiles.")).toBeInTheDocument();
  });
});
