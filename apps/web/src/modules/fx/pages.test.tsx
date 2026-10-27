import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const rateTypes = [{ id: "t1", code: "CLOSING", name: "Closing", allowInverse: true }];

function answer(extra: (url: URL, method: string) => ReturnType<typeof json> | undefined) {
  server.handler = (url, method) => (url.pathname === "/api/v1/fx/rate-types" ? json(200, rateTypes) : extra(url, method));
}

async function fill(label: string, value: string) {
  fireEvent.change(await screen.findByLabelText(label), { target: { value } });
}

async function chooseType() {
  await screen.findByRole("option", { name: "CLOSING – Closing" });
  fireEvent.change(screen.getByLabelText("Rate type"), { target: { value: "CLOSING" } });
}

describe("rates page", () => {
  it("shows the stored versions of a pair, marking superseded ones", async () => {
    answer((url) =>
      url.pathname === "/api/v1/fx/rates/history"
        ? json(200, {
            entries: [
              { id: "a", version: 1, validFrom: "2026-01-02", rate: "1.1000000000", source: "ecb", supersededBy: "b", createdAt: "2026-01-02T09:00:00Z" },
              { id: "b", version: 2, validFrom: "2026-01-02", rate: "1.2000000000", source: "manual", createdAt: "2026-01-03T10:30:00Z", createdBy: "u1" },
            ],
          })
        : undefined,
    );
    const { container } = renderAt("/fx/rates");
    await chooseType();
    await fill("From currency", "eur");
    await fill("To currency", "usd");
    fireEvent.click(screen.getByRole("button", { name: "Show rates" }));
    const table = await screen.findByRole("table", { name: "Rate history" });
    expect(await within(table).findByText("1.2000000000")).toBeInTheDocument();
    expect(within(table).getByText("Superseded")).toBeInTheDocument();
    expect(within(table).getByText("Active")).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname.endsWith("/history"))?.url.search).toBe("?type=CLOSING&from=EUR&to=USD");
    expect(pageViolations(container)).toEqual([]);
  });

  it("validates the filters before calling the API", async () => {
    answer(() => undefined);
    renderAt("/fx/rates");
    await screen.findByRole("option", { name: "CLOSING – Closing" });
    await fill("From currency", "EU");
    fireEvent.click(screen.getByRole("button", { name: "Show rates" }));
    expect(await screen.findByText("Choose a rate type.")).toBeInTheDocument();
    expect(screen.getAllByText("Use three capital letters, for example EUR.")).toHaveLength(1);
    expect(server.calls.some((c) => c.url.pathname.endsWith("/history"))).toBe(false);
  });

  it("explains an empty history", async () => {
    answer((url) => (url.pathname.endsWith("/history") ? json(200, { entries: [] }) : undefined));
    renderAt("/fx/rates");
    await chooseType();
    await fill("From currency", "EUR");
    await fill("To currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Show rates" }));
    expect(await screen.findByText("No rates for this pair")).toBeInTheDocument();
  });
});

describe("lookup page", () => {
  it("shows the resolved rate, how it was found and the rates used", async () => {
    answer((url) =>
      url.pathname === "/api/v1/fx/rates"
        ? json(200, {
            rateType: "CLOSING", from: "USD", to: "GBP", date: "2026-01-02", path: "triangulated", rate: "0.64",
            hops: [
              { from: "USD", to: "EUR", rate: "0.8", validFrom: "2026-01-02" },
              { from: "EUR", to: "GBP", rate: "0.8", validFrom: "2026-01-02" },
            ],
          })
        : undefined,
    );
    const { container } = renderAt("/fx/lookup");
    await chooseType();
    await fill("From currency", "USD");
    await fill("To currency", "GBP");
    await fill("Reference currency", "EUR");
    fireEvent.click(screen.getByRole("button", { name: "Look up rate" }));
    expect(await screen.findByText("1 USD = 0.640000 GBP")).toHaveAttribute("title", "0.64");
    expect(screen.getByText(/Triangulated/)).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Rates used" })).getAllByRole("row")).toHaveLength(3);
    expect(server.calls.find((c) => c.url.pathname === "/api/v1/fx/rates")?.url.search).toBe("?type=CLOSING&from=USD&to=GBP&via=EUR");
    expect(pageViolations(container)).toEqual([]);
  });

  it("announces a missing rate", async () => {
    answer((url) => (url.pathname === "/api/v1/fx/rates" ? json(422, { code: "LEDGER_RATE_MISSING", detail: "LEDGER_RATE_MISSING: type CLOSING pair USD/JPY" }) : undefined));
    renderAt("/fx/lookup");
    await chooseType();
    await fill("From currency", "USD");
    await fill("To currency", "JPY");
    fireEvent.click(screen.getByRole("button", { name: "Look up rate" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("No rate found");
    expect(alert.textContent).toContain("LEDGER_RATE_MISSING");
  });

  it("uses a generic title for other failures", async () => {
    answer((url) => (url.pathname === "/api/v1/fx/rates" ? json(500, { code: "COM-0500", detail: "internal error" }) : undefined));
    renderAt("/fx/lookup");
    await chooseType();
    await fill("From currency", "USD");
    await fill("To currency", "JPY");
    fireEvent.click(screen.getByRole("button", { name: "Look up rate" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Could not look up the rate");
  });

  it("rejects a malformed reference currency locally", async () => {
    answer(() => undefined);
    renderAt("/fx/lookup");
    await chooseType();
    await fill("From currency", "USD");
    await fill("To currency", "JPY");
    await fill("Reference currency", "e");
    fireEvent.click(screen.getByRole("button", { name: "Look up rate" }));
    expect(await screen.findByText("Use three capital letters, for example EUR.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname === "/api/v1/fx/rates")).toBe(false);
  });
});

describe("convert page", () => {
  it("posts the amount and shows the rounded result", async () => {
    answer((url, method) =>
      url.pathname === "/api/v1/fx/convert" && method === "POST"
        ? json(200, {
            rateType: "CLOSING", date: "2026-01-02", to: "USD",
            results: [{ amount: { value: "100", currency: "EUR" }, converted: { value: "108.55", currency: "USD" }, path: "direct", rate: "1.0855" }],
          })
        : undefined,
    );
    const { container } = renderAt("/fx/convert");
    await chooseType();
    await fill("Amount", "100");
    await fill("From currency", "EUR");
    await fill("To currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Convert" }));
    expect(await screen.findByText("108.55 USD")).toBeInTheDocument();
    expect(screen.getByText("1.085500 (direct) on 2026-01-02")).toHaveAttribute("title", "1.0855");
    expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ rateType: "CLOSING", to: "USD", amounts: [{ value: "100", currency: "EUR" }] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("blocks a non-numeric amount before calling the API", async () => {
    answer(() => undefined);
    renderAt("/fx/convert");
    await chooseType();
    await fill("Amount", "12,5");
    await fill("From currency", "EUR");
    await fill("To currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Convert" }));
    expect(await screen.findByText("Enter a number such as 1250.50.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("maps server field errors back onto the form fields", async () => {
    answer((url) => (url.pathname === "/api/v1/fx/convert" ? json(400, { code: "COM-0001", detail: "request failed validation", errors: { "amounts[0].value": "must be a decimal number in a string" } }) : undefined));
    renderAt("/fx/convert");
    await chooseType();
    await fill("Amount", "1.12345678901");
    await fill("From currency", "EUR");
    await fill("To currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Convert" }));
    expect(await screen.findByText("must be a decimal number in a string")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(true);
  });

  it("shows a server rejection that no field owns", async () => {
    answer((url) => (url.pathname === "/api/v1/fx/convert" ? json(422, { code: "FX_RATE_DATE_FUTURE", detail: "FX_RATE_DATE_FUTURE: 2999-01-01" }) : undefined));
    renderAt("/fx/convert");
    await chooseType();
    await fill("Amount", "5");
    await fill("From currency", "EUR");
    await fill("To currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Convert" }));
    expect((await screen.findByRole("alert")).textContent).toContain("FX_RATE_DATE_FUTURE");
  });
});
