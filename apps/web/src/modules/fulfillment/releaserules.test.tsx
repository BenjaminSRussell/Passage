import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ReleaseRule } from "./api/client";
import { validateReleaseRule } from "./ReleaseRulesPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0195f0a0-0000-7000-8000-000000000007";
const WH2 = "0195f0a0-0000-7000-8000-000000000008";
const LE = "0195f0a0-0000-7000-8000-0000000000aa";

const calls = (method: string, suffix: string) => server.calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const SUPERVISOR = ["WarehouseView", "WarehouseRelease"];

const rules: ReleaseRule[] = [
  { warehouseId: WH, legalEntityId: LE, horizonDays: 2, requireFullAtp: true, waveMaxLines: 200, waveMaxOrders: 50, version: 0, updatedAt: null },
  { warehouseId: WH2, legalEntityId: LE, horizonDays: 5, requireFullAtp: false, waveMaxLines: 80, waveMaxOrders: 12, version: 3, updatedAt: "2026-11-01T08:00:00Z" },
];

describe("release rules page (FR-28.1, FR-28.13)", () => {
  it("lists the rules, opens one, saves it with its version and reloads", async () => {
    let current = rules;
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        current = [rules[0], { ...rules[1], horizonDays: 9, waveMaxLines: 150, version: 4 }];
        return json(200, current[1]);
      }
      if (url.pathname === "/api/v1/fulfillment/release-rules") return json(200, { items: current });
      return undefined;
    };
    const { container } = renderAt("/fulfillment/release-rules", SUPERVISOR);
    const table = await findTable({ name: "Release rules" });
    expect(within(table).getByText("Partial ATP allowed")).toBeInTheDocument();
    expect(within(table).getByText("Defaults")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: WH2 }));
    expect(screen.getByLabelText(/Most lines in a wave/)).toHaveValue("80");
    fireEvent.change(screen.getByLabelText(/Release horizon/), { target: { value: "9" } });
    fireEvent.change(screen.getByLabelText(/Most lines in a wave/), { target: { value: "150" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/Saved: release 9 days ahead, waves of at most 150 lines and 12 orders/)).toBeInTheDocument();
    const put = calls("PUT", WH2)[0];
    expect(header(put, "If-Match")).toBe('"3"');
    expect(idempotencyKey(put)).toBeTruthy();
    expect(put.body).toEqual({ legalEntityId: LE, horizonDays: 9, requireFullAtp: false, waveMaxLines: 150, waveMaxOrders: 12 });
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Release rules" })).getByText("9")).toBeInTheDocument());
  });

  it("validates before calling the service", () => {
    const good = { warehouseId: WH, legalEntityId: LE, horizonDays: "0", requireFullAtp: true, waveMaxLines: "2000", waveMaxOrders: "500" };
    expect(validateReleaseRule(good).input?.body).toEqual({ legalEntityId: LE, horizonDays: 0, requireFullAtp: true, waveMaxLines: 2000, waveMaxOrders: 500 });
    const bad = validateReleaseRule({ warehouseId: "nope", legalEntityId: "", horizonDays: "31", requireFullAtp: true, waveMaxLines: "0", waveMaxOrders: "501" });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["horizonDays", "legalEntityId", "warehouseId", "waveMaxLines", "waveMaxOrders"]);
    expect(validateReleaseRule({ ...good, horizonDays: "1.5" }).errors.horizonDays).toBeTruthy();
  });

  it("shows field errors and sends nothing, and explains a stale version and another legal entity", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        const stale = calls("PUT", "").length === 1;
        return json(stale ? 412 : 422, { type: "about:blank", title: "x", status: stale ? 412 : 422, code: stale ? "conflict_version" : "FUL-0057", detail: "refused" });
      }
      if (url.pathname === "/api/v1/fulfillment/release-rules") return json(200, { items: rules });
      return undefined;
    };
    renderAt("/fulfillment/release-rules", SUPERVISOR);
    const table = await findTable({ name: "Release rules" });
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/Enter the warehouse id/)).toBeInTheDocument();
    expect(calls("PUT", "")).toHaveLength(0);
    fireEvent.click(within(table).getByRole("button", { name: WH2 }));
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/Someone else changed this rule/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText(/belongs to another legal entity/)).toBeInTheDocument();
  });

  it("keeps a typed warehouse's saved version and reads only for a viewer", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(200, { ...rules[1], version: 4 });
      if (url.pathname === "/api/v1/fulfillment/release-rules") return json(200, { items: rules });
      return undefined;
    };
    const { unmount } = renderAt("/fulfillment/release-rules", SUPERVISOR);
    await findTable({ name: "Release rules" });
    fireEvent.change(screen.getByLabelText(/Warehouse id/), { target: { value: WH2 } });
    fireEvent.change(screen.getByLabelText(/Legal entity id/), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    await screen.findByText(/Saved:/);
    expect(header(calls("PUT", WH2)[0], "If-Match")).toBe('"3"');
    unmount();

    renderAt("/fulfillment/release-rules", ["WarehouseView"]);
    const table = await findTable({ name: "Release rules" });
    expect(within(table).getByText("Partial ATP allowed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save rule" })).not.toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: WH2 })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the WarehouseRelease duty/)).toBeInTheDocument();
  });

  it("shows the empty state and the error with retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "down" }) : json(200, { items: [] }));
    renderAt("/fulfillment/release-rules");
    expect((await findAlert()).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No warehouses yet")).toBeInTheDocument();
  });
});
