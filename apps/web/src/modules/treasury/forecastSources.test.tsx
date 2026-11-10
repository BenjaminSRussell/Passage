import { screen } from "@testing-library/react";
import { describe, it } from "vitest";
import type { Forecast } from "./api/client";
import { json, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";

describe("cash forecast sources", () => {
  it("says what the live AP and AR sources contributed and warns when one is missing or partial", async () => {
    const forecast: Forecast = {
      layout: "daily",
      asOf: "2026-11-01",
      buckets: [{ key: "2026-11-05", currency: "USD", inflow: "100.00", outflow: "-40.00", net: "60.00", cumulative: "60.00" }],
      outOfHorizon: 0,
      sources: [
        { type: "AP", status: "ok", count: 12 },
        { type: "AR", status: "unavailable", count: 0 },
      ],
    };
    server.handler = (url) => (url.pathname === "/api/v1/treasury/cash-forecast" ? json(200, forecast) : undefined);
    renderAt(`/treasury/cash-forecast?legalEntityId=${LE}`);
    await screen.findByText("Includes 12 open vendor invoices from payables, on their due dates.");
    await screen.findByText(/Receivables did not answer/);
  });
});
