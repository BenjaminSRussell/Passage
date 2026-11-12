import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import ops from "./module";

useFakeServer();
afterEach(() => setLookupFetcher(null));

const now = Date.now();
const iso = (offsetMin: number) => new Date(now + offsetMin * 60_000).toISOString();
const operator = "0a1b2c3d-0000-4000-8000-000000000001";

function handle(url: URL) {
  switch (url.pathname) {
    case "/api/v1/ops/regions":
      return json(200, {
        items: [
          { name: "us-east", role: "PRIMARY", lagSeconds: 0 },
          { name: "us-west", role: "PASSIVE", lagSeconds: 1073741824, lagStale: true },
          { name: "eu-west", role: "FENCED", lagSeconds: 12 },
        ],
        maxLagSeconds: 300,
      });
    case "/api/v1/ops/break-glass":
      return json(200, {
        items: [
          { id: "g1", userId: operator, reasonCode: "REGION_LOSS", ticketRef: "INC-42", grantedAt: iso(-10), expiresAt: iso(50), reviewDueAt: iso(4000), uses: 1 },
          { id: "g2", userId: operator, reasonCode: "PRODUCTION_OUTAGE", ticketRef: "INC-7", grantedAt: iso(-9000), expiresAt: iso(-8900), reviewDueAt: iso(-100), uses: 0 },
        ],
      });
    case "/api/v1/identity/user-names":
      return json(200, { items: [{ userId: operator, displayName: "Tom Becker" }] });
    case "/api/v1/ops/slo":
      return json(200, {
        window: url.searchParams.get("window"),
        generatedAt: iso(0),
        breached: 1,
        services: [
          { service: "billing", requests: 1000, availability: 0.998, availabilityTarget: 0.999, latencyP95Ms: 140, latencyTargetMs: 2000, errorBudgetRemaining: -1, availabilityBreached: true, latencyBreached: false, breached: true, breachedSince: iso(-30), noTraffic: false },
          { service: "ledger", requests: 5000, availability: 1, availabilityTarget: 0.999, latencyP95Ms: 80, latencyTargetMs: 2000, errorBudgetRemaining: 1, availabilityBreached: false, latencyBreached: false, breached: false, noTraffic: false },
        ],
      });
    case "/api/v1/ops/licenses":
      return json(200, {
        items: [
          {
            license: { tenantId: "t1", issuedAt: "2026-01-01", expiresAt: "2027-01-01", fullUsers: 10, operationalUsers: 5, selfserviceUsers: 50 },
            version: 1,
            status: { state: "GRACE", daysToExpiry: 50, codes: ["LIC-1802"] },
            messages: [],
            usage: { activeFull: 12, activeOperational: 2, activeSelfservice: 9, countedAt: iso(-5), lastState: "GRACE" },
            tenantName: "Cedar Ridge Pumps",
          },
          {
            license: { tenantId: "t2", issuedAt: "2025-01-01", expiresAt: "2025-12-31", fullUsers: 5, operationalUsers: 0, selfserviceUsers: 0 },
            version: 1,
            status: { state: "EXPIRED", daysToExpiry: -300, codes: ["LIC-1803"] },
            messages: [],
            usage: { activeFull: 0, activeOperational: 0, activeSelfservice: 0 },
            tenantName: "",
          },
        ],
      });
  }
  return undefined;
}

describe("operations console", () => {
  it("lists every screen under the operator-only view duty", () => {
    expect(ops.nav.map((n) => n.duty)).toEqual(["ops.region.view", "ops.region.view", "ops.region.view", "ops.region.view"]);
  });

  it("shows which region is primary and whether a standby can take over", async () => {
    server.handler = handle;
    const { container } = renderRoutes(ops.routes, "/ops");
    const table = await screen.findByRole("table", { name: "Regions" });
    expect(await within(table).findByText("Primary")).toBeInTheDocument();
    expect(within(table).getByText("Unknown (no recent report)")).toBeInTheDocument();
    expect(within(table).getByText("Only by accepting data loss")).toBeInTheDocument();
    expect(within(table).getByText("Fenced")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("names the operator of each break-glass window and flags an overdue review", async () => {
    server.handler = handle;
    setLookupFetcher(async (url) => {
      const reply = handle(new URL(url, "http://localhost"));
      return new Response(JSON.stringify(reply?.body ?? {}), { status: reply?.status ?? 404 });
    });
    renderRoutes(ops.routes, "/ops/break-glass");
    const table = await screen.findByRole("table", { name: "Break-glass windows" });
    expect(await within(table).findByText("Open")).toBeInTheDocument();
    expect((await within(table).findAllByText("Tom Becker")).length).toBeGreaterThan(0);
    expect(within(table).getByText("Review overdue")).toBeInTheDocument();
    expect(within(table).getByText("Region loss")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(operator);
  });

  it("reports the service levels for the period picked", async () => {
    server.handler = handle;
    renderRoutes(ops.routes, "/ops/slo");
    const table = await screen.findByRole("table", { name: "Service levels" });
    expect(await within(table).findByText("99.800 %")).toBeInTheDocument();
    expect(within(table).getByText(/^Missed since/)).toBeInTheDocument();
    expect(within(table).getByText("Met")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "7d" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("window") === "7d")).toBe(true));
  });

  it("explains an SLO report that monitoring can't answer", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/ops/slo" ? json(503, { code: "OPS-1725", detail: "set PROMETHEUS_URL" }) : undefined);
    renderRoutes(ops.routes, "/ops/slo");
    expect(await screen.findByText(/The service level report can't be read right now/)).toBeInTheDocument();
  });

  it("lists licences by company name with their users and warns about an expired one", async () => {
    server.handler = handle;
    renderRoutes(ops.routes, "/ops/licenses");
    const table = await screen.findByRole("table", { name: "Licences" });
    expect(await within(table).findByText("Cedar Ridge Pumps")).toBeInTheDocument();
    expect(within(table).getByText("12 of 10")).toBeInTheDocument();
    expect(within(table).getByText("Grace period")).toBeInTheDocument();
    expect(within(table).getByText("Provisioned outside the console")).toBeInTheDocument();
    expect(screen.getByText(/One licence has expired/)).toBeInTheDocument();
  });
});
