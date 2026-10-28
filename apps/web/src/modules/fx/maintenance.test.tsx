import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { json, pageViolations, renderAt, server, useFakeServer, type Reply } from "./testServer";

useFakeServer();

const closing = { id: "t1", code: "CLOSING", name: "Closing", allowInverse: true };
const average = { id: "t2", code: "AVERAGE", name: "Monthly average", allowInverse: false, provider: "ecb" };

function history(rate: string, extra: Record<string, unknown> = {}) {
  return { id: "h1", version: 1, validFrom: "2026-01-02", rate, source: "manual", createdAt: "2026-01-02T09:00:00Z", ...extra };
}

function answer(routes: { history?: Reply; publish?: Reply; put?: Reply; list?: Reply }) {
  server.handler = (url, method) => {
    if (url.pathname === "/api/v1/fx/rate-types" && method === "GET") return routes.list ?? json(200, [closing, average]);
    if (url.pathname.startsWith("/api/v1/fx/rate-types/") && method === "PUT") return routes.put;
    if (url.pathname === "/api/v1/fx/rates/history") return routes.history ?? json(200, { entries: [] });
    if (url.pathname === "/api/v1/fx/rates" && method === "POST") return routes.publish;
    return undefined;
  };
}

const posted = (version = 2, unchanged = false) => json(200, { rates: [{ id: "r1", version, superseded: false, unchanged }] });

async function fillEntry(user: ReturnType<typeof userEvent.setup>, rate: string) {
  await screen.findByRole("option", { name: "CLOSING – Closing" });
  await user.selectOptions(screen.getByLabelText("Rate type"), "CLOSING");
  await user.type(screen.getByLabelText("From currency"), "eur");
  await user.type(screen.getByLabelText("To currency"), "usd");
  await user.type(screen.getByLabelText("Valid from"), "2026-02-01");
  await user.type(screen.getByLabelText("Rate"), rate);
}

const publishCalls = () => server.calls.filter((c) => c.method === "POST");

describe("rate entry page", () => {
  it("saves a first rate for a pair without asking for confirmation", async () => {
    answer({ publish: posted(1) });
    const user = userEvent.setup();
    const { container } = renderAt("/fx/rate-entry");
    await fillEntry(user, "1.0855");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Stored as version 1.")).toBeInTheDocument();
    expect(publishCalls()).toHaveLength(1);
    expect(publishCalls()[0].body).toEqual({ rates: [{ rateType: "CLOSING", from: "EUR", to: "USD", validFrom: "2026-02-01", rate: "1.0855" }] });
    expect(publishCalls()[0].headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(screen.getByLabelText("Rate")).toHaveValue("");
    expect(pageViolations(container)).toEqual([]);
  });

  it("saves without confirmation when the change is exactly 10 percent", async () => {
    answer({ history: json(200, { entries: [history("1.0000")] }), publish: posted() });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.1000");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Stored as version 2.")).toBeInTheDocument();
    expect(screen.queryByText(/of the previous rate/)).not.toBeInTheDocument();
  });

  it("asks for confirmation above 10 percent and saves only after it is given", async () => {
    answer({ history: json(200, { entries: [history("1.0000")] }), publish: posted() });
    const user = userEvent.setup();
    const { container } = renderAt("/fx/rate-entry");
    await fillEntry(user, "1.1200");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    const warning = await screen.findByRole("alert");
    expect(warning.textContent).toContain("12.0% above the previous rate");
    expect(warning.textContent).toContain("EUR to USD was 1.0000 from 2026-01-02");
    expect(publishCalls()).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
    await user.click(within(warning).getByRole("button", { name: "Confirm and save" }));
    expect(await screen.findByText("Stored as version 2.")).toBeInTheDocument();
    expect(publishCalls()).toHaveLength(1);
  });

  it("flags a drop of more than 10 percent and lets the user go back without saving", async () => {
    answer({ history: json(200, { entries: [history("1.0000")] }), publish: posted() });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "0.8000");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect((await screen.findByRole("alert")).textContent).toContain("20.0% below the previous rate");
    await user.click(screen.getByRole("button", { name: "Go back and edit" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Rate")).toHaveValue("0.8000");
    expect(publishCalls()).toHaveLength(0);
  });

  it("compares against the newest rate that is not superseded", async () => {
    answer({
      history: json(200, { entries: [history("9.0000", { id: "old", supersededBy: "new" }), history("1.0000", { id: "new", version: 2 })] }),
      publish: posted(),
    });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.0500");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Stored as version 2.")).toBeInTheDocument();
  });

  it("validates locally and focuses the first invalid field", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await screen.findByRole("option", { name: "CLOSING – Closing" });
    await user.type(screen.getByLabelText("From currency"), "EU");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Choose a rate type.")).toBeInTheDocument();
    expect(screen.getByText("Use three capital letters, for example EUR.")).toBeInTheDocument();
    expect(screen.getByText("Enter the date the rate takes effect.")).toBeInTheDocument();
    expect(screen.getByText("Enter a rate greater than zero, such as 1.0855.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Rate type")).toHaveFocus());
    expect(screen.getByLabelText("Rate type")).toHaveAccessibleDescription("Choose a rate type.");
    expect(server.calls.some((c) => c.url.pathname.includes("/rates"))).toBe(false);
  });

  it("rejects a zero rate", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "0.0");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Enter a rate greater than zero, such as 1.0855.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Rate")).toHaveFocus());
  });

  it("maps server field errors onto the fields and focuses the first", async () => {
    answer({ publish: json(400, { code: "COM-0001", detail: "request failed validation", errors: { "rates[0].validFrom": "must be a date as YYYY-MM-DD", "rates[0].rate": "must be a decimal number in a string" } }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("must be a date as YYYY-MM-DD")).toBeInTheDocument();
    expect(screen.getByText("must be a decimal number in a string")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Valid from")).toHaveFocus());
  });

  it("explains a missing permission", async () => {
    answer({ publish: json(403, { code: "COM-0403", detail: "forbidden" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("You do not have permission");
    expect(alert.textContent).toContain("Maintain exchange rates");
  });

  it("retries a conflicting save with the same idempotency key, and a new one after an edit", async () => {
    let attempts = 0;
    answer({});
    const base = server.handler;
    server.handler = (url, method) => {
      if (method === "POST") return ++attempts === 1 ? json(409, { code: "FX_IDEMPOTENCY_IN_PROGRESS", detail: "in progress" }) : posted();
      return base(url, method);
    };
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Another change is still in progress");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Stored as version 2.")).toBeInTheDocument();
    const [first, second] = publishCalls();
    expect(first.headers["Idempotency-Key"]).toBe(second.headers["Idempotency-Key"]);
    await user.type(screen.getByLabelText("Rate"), "1.3");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await waitFor(() => expect(publishCalls()).toHaveLength(3));
    expect(publishCalls()[2].headers["Idempotency-Key"]).not.toBe(first.headers["Idempotency-Key"]);
  });

  it("reports that an identical rate was already stored", async () => {
    answer({ publish: posted(1, true) });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("This rate was already stored, so nothing changed.")).toBeInTheDocument();
  });

  it("does not save when the previous rate cannot be loaded", async () => {
    answer({ history: json(500, { code: "COM-0500", detail: "internal error" }), publish: posted() });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Could not compare with the previous rate");
    expect(publishCalls()).toHaveLength(0);
  });
});

describe("rate types page", () => {
  it("lists the rate types with their provider and inverse setting", async () => {
    answer({});
    const { container } = renderAt("/fx/rate-types");
    const table = await screen.findByRole("table", { name: "Rate types" });
    expect(await within(table).findByText("Monthly average")).toBeInTheDocument();
    expect(within(table).getByText("ecb")).toBeInTheDocument();
    expect(within(table).getByText("Manual entry")).toBeInTheDocument();
    expect(within(table).getByText("Not allowed")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an empty list", async () => {
    answer({ list: json(200, []) });
    renderAt("/fx/rate-types");
    expect(await screen.findByText("No rate types yet")).toBeInTheDocument();
  });

  it("shows a skeleton while loading", () => {
    answer({});
    renderAt("/fx/rate-types");
    expect(screen.getByRole("table", { name: "Rate types" })).toHaveAttribute("aria-busy", "true");
  });

  it("explains when the list cannot be read", async () => {
    answer({ list: json(403, { code: "COM-0403", detail: "forbidden" }) });
    renderAt("/fx/rate-types");
    const alert = await screen.findByText("You do not have permission");
    expect(alert).toBeInTheDocument();
    expect(screen.getByText(/View exchange rates/)).toBeInTheDocument();
  });

  it("shows a generic error when the list fails", async () => {
    answer({ list: json(500, { code: "COM-0500", detail: "internal error" }) });
    renderAt("/fx/rate-types");
    expect(await screen.findByText("Could not load the rate types")).toBeInTheDocument();
  });

  it("creates a rate type", async () => {
    answer({ put: json(200, { id: "t3", code: "BUDGET", name: "Budget", allowInverse: false, provider: "csv" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.type(await screen.findByLabelText("Code"), "budget");
    await user.type(screen.getByLabelText("Name"), "Budget");
    await user.type(screen.getByLabelText("Provider"), "csv");
    await user.click(screen.getByLabelText("Allow inverse rates"));
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect(await screen.findByText("Rate type BUDGET saved")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe("/api/v1/fx/rate-types/BUDGET");
    expect(put?.body).toEqual({ name: "Budget", allowInverse: false, provider: "csv" });
    expect(screen.getByLabelText("Code")).toHaveValue("");
  });

  it("edits an existing rate type and keeps its code fixed", async () => {
    answer({ put: json(200, { ...average, name: "Average" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    expect(screen.getByLabelText("Code")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Name")).toHaveValue("Monthly average");
    expect(screen.getByLabelText("Provider")).toHaveValue("ecb");
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "Average");
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Rate type AVERAGE saved")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "PUT")?.body).toEqual({ name: "Average", allowInverse: false, provider: "ecb" });
  });

  it("validates locally and focuses the first invalid field", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "Create rate type" }));
    expect(await screen.findByText("Enter a code, for example CLOSING.")).toBeInTheDocument();
    expect(screen.getByText("Enter a name, for example Month-end closing.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Code")).toHaveFocus());
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("explains a missing permission to save", async () => {
    answer({ put: json(403, { code: "COM-0403", detail: "forbidden" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.type(await screen.findByLabelText("Code"), "X");
    await user.type(screen.getByLabelText("Name"), "Xtra");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    const alert = await screen.findByText("You do not have permission");
    expect(alert).toBeInTheDocument();
    expect(screen.getByText(/Maintain exchange rates/)).toBeInTheDocument();
  });

  it("shows the server's rejection when no field owns it", async () => {
    answer({ put: json(400, { code: "FX_RATE_INVALID", detail: "rate type code and name are required" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.type(await screen.findByLabelText("Code"), "X");
    await user.type(screen.getByLabelText("Name"), "Xtra");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect((await screen.findByRole("alert")).textContent).toContain("rate type code and name are required");
  });

  it("returns to an empty form with New rate type", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "CLOSING" }));
    await user.click(screen.getByRole("button", { name: "New rate type" }));
    expect(screen.getByLabelText("Code")).toHaveValue("");
    expect(screen.getByLabelText("Code")).not.toHaveAttribute("readonly");
  });
});
