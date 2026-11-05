import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { json, pageViolations, renderAt, server, useFakeServer, type Reply } from "./testServer";
import { emptyEntry, validateEntry } from "./validate";

useFakeServer();

const closing = { id: "t1", code: "CLOSING", name: "Closing", allowInverse: true };
const average = { id: "t2", code: "AVERAGE", name: "Monthly average", allowInverse: false, provider: "ecb" };

function history(rate: string, extra: Record<string, unknown> = {}) {
  return { id: "h1", version: 1, validFrom: "2026-01-02", rate, source: "manual", createdAt: "2026-01-02T09:00:00Z", ...extra };
}

function answer(routes: { history?: Reply; publish?: Reply; put?: Reply; create?: Reply; list?: Reply; importRun?: Reply }) {
  server.handler = (url, method) => {
    if (url.pathname === "/api/v1/fx/rate-types" && method === "GET") return routes.list ?? json(200, [closing, average]);
    if (url.pathname === "/api/v1/fx/rate-types" && method === "POST") return routes.create;
    if (url.pathname.startsWith("/api/v1/fx/rate-types/") && method === "PUT") return routes.put;
    if (url.pathname === "/api/v1/fx/rates/history") return routes.history ?? json(200, { entries: [] });
    if (url.pathname === "/api/v1/fx/rates" && method === "POST") return routes.publish;
    if (url.pathname === "/api/v1/fx/providers/ecb:import" && method === "POST") return routes.importRun;
    return undefined;
  };
}

const posted = (version = 2, unchanged = false) => json(201, { rates: [{ id: "r1", version, superseded: false, unchanged }] });

async function fillEntry(user: ReturnType<typeof userEvent.setup>, rate: string) {
  await screen.findByRole("option", { name: "CLOSING – Closing" });
  await user.selectOptions(screen.getByLabelText("Rate type"), "CLOSING");
  await user.type(screen.getByLabelText("From currency"), "eur");
  await user.type(screen.getByLabelText("To currency"), "usd");
  await user.type(screen.getByLabelText("Valid from"), "2026-02-01");
  await user.type(screen.getByLabelText("Rate"), rate);
}

function holdRequests(method: string) {
  let release: () => void = () => undefined;
  const gate = new Promise<void>((resolve) => (release = resolve));
  const stub = globalThis.fetch;
  vi.stubGlobal("fetch", async (input: string, init?: RequestInit) => {
    if (init?.method === method) await gate;
    return stub(input, init);
  });
  return release;
}

const rateTypeCalls = (method: string) => server.calls.filter((c) => c.method === method && c.url.pathname.startsWith("/api/v1/fx/rate-types"));
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
    expect(screen.queryByText(/the previous rate/)).not.toBeInTheDocument();
  });

  it("asks for confirmation above 10 percent, moves focus to Confirm, and saves only after it is given", async () => {
    answer({ history: json(200, { entries: [history("1")] }), publish: posted() });
    const user = userEvent.setup();
    const { container } = renderAt("/fx/rate-entry");
    await fillEntry(user, "1.1200");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("This rate is 12.00% above the previous rate")).toBeInTheDocument();
    expect(screen.getByText(/EUR to USD was 1.000000 from 2026-01-02/)).toBeInTheDocument();
    const confirm = screen.getByRole("button", { name: "Confirm and save" });
    await waitFor(() => expect(confirm).toHaveFocus());
    expect(publishCalls()).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
    await user.click(confirm);
    expect(await screen.findByText("Stored as version 2.")).toBeInTheDocument();
    expect(publishCalls()).toHaveLength(1);
  });

  it("keeps Save rate aria-disabled, not natively disabled, while the warning shows", async () => {
    answer({ history: json(200, { entries: [history("1")] }), publish: posted() });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.5");
    const save = screen.getByRole("button", { name: "Save rate" });
    await user.click(save);
    await screen.findByText(/above the previous rate/);
    expect(save).toHaveAttribute("aria-disabled", "true");
    expect(save).not.toBeDisabled();
    await user.click(save);
    expect(server.calls.filter((c) => c.url.pathname.endsWith("/history"))).toHaveLength(1);
  });

  it("flags a drop of more than 10 percent and returns focus to the rate field on Go back", async () => {
    answer({ history: json(200, { entries: [history("1")] }), publish: posted() });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "0.8000");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("This rate is 20.00% below the previous rate")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Go back and edit" }));
    expect(screen.queryByText(/the previous rate/)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Rate")).toHaveFocus();
    expect(screen.getByLabelText("Rate")).toHaveValue("0.8000");
    expect(publishCalls()).toHaveLength(0);
  });

  it("compares a backdated correction with the rate in force on its own date, not a later one", async () => {
    answer({
      history: json(200, { entries: [history("1.08", { id: "jan", validFrom: "2026-01-05" }), history("1.1", { id: "feb", validFrom: "2026-02-01" })] }),
      publish: posted(),
    });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await screen.findByRole("option", { name: "CLOSING – Closing" });
    await user.selectOptions(screen.getByLabelText("Rate type"), "CLOSING");
    await user.type(screen.getByLabelText("From currency"), "EUR");
    await user.type(screen.getByLabelText("To currency"), "USD");
    await user.type(screen.getByLabelText("Valid from"), "2026-01-05");
    await user.type(screen.getByLabelText("Rate"), "1.20");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("This rate is 11.11% above the previous rate")).toBeInTheDocument();
    expect(screen.getByText(/was 1.080000 from 2026-01-05/)).toBeInTheDocument();
    expect(publishCalls()).toHaveLength(0);
  });

  it("disables the fields while a save is in flight so the posted values cannot change", async () => {
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => (release = resolve));
    answer({});
    const base = server.handler;
    server.handler = (url, method) => (method === "POST" ? posted(1) : base(url, method));
    const stub = globalThis.fetch;
    vi.stubGlobal("fetch", async (input: string, init?: RequestInit) => {
      if (init?.method === "POST") await gate;
      return stub(input, init);
    });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await waitFor(() => expect(screen.getByLabelText("Rate")).toBeDisabled());
    expect(screen.getByLabelText("From currency")).toBeDisabled();
    expect(screen.getByLabelText("Rate type")).toBeDisabled();
    release();
    expect(await screen.findByText("Stored as version 1.")).toBeInTheDocument();
    expect(screen.getByLabelText("Rate")).toBeEnabled();
    expect(publishCalls()).toHaveLength(1);
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

  it("clears a field's error and invalid state as soon as it is edited, without moving focus", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await screen.findByRole("option", { name: "CLOSING – Closing" });
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await screen.findByText("Enter a rate greater than zero, such as 1.0855.");
    await user.type(screen.getByLabelText("Rate"), "1");
    expect(screen.queryByText("Enter a rate greater than zero, such as 1.0855.")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Rate")).not.toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Rate")).toHaveFocus();
    expect(screen.getByLabelText("Rate type")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText("Choose a rate type.")).toBeInTheDocument();
  });

  it("moves focus to the rate field after a successful save, also after confirming a large change", async () => {
    answer({
      history: json(200, { entries: [history("1")] }),
      publish: posted(),
    });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.5");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await user.click(await screen.findByRole("button", { name: "Confirm and save" }));
    await screen.findByText("Stored as version 2.");
    await waitFor(() => expect(screen.getByLabelText("Rate")).toHaveFocus());
  });

  it("moves focus to Save rate when a confirmed save fails with an error no field owns", async () => {
    answer({
      history: json(200, { entries: [history("1")] }),
      publish: json(500, { code: "COM-0500", detail: "internal error" }),
    });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.5");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await user.click(await screen.findByRole("button", { name: "Confirm and save" }));
    await screen.findByText("Could not save the rate");
    await waitFor(() => expect(screen.getByRole("button", { name: "Save rate" })).toHaveFocus());
  });

  it("disables Go back and edit while the confirmed save is in flight", async () => {
    answer({
      history: json(200, { entries: [history("1")] }),
      publish: posted(),
    });
    const release = holdRequests("POST");
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.5");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await user.click(await screen.findByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Go back and edit" })).toBeDisabled());
    release();
    expect(await screen.findByText("Stored as version 2.")).toBeInTheDocument();
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
    expect(alert.textContent).toContain("fx.rate.maintain");
  });

  it("retries a conflicting save with the same idempotency key, and a new one after an edit", async () => {
    let attempts = 0;
    answer({});
    const base = server.handler;
    server.handler = (url, method) => {
      if (method === "POST") return ++attempts === 1 ? { ...json(409, { code: "FX_IDEMPOTENCY_IN_PROGRESS", detail: "in progress" }), headers: { "Retry-After": "1" } } : posted();
      return base(url, method);
    };
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    const conflict = await screen.findByRole("alert");
    expect(conflict.textContent).toContain("This rate is still being saved");
    expect(conflict.textContent).toContain("Wait 1 second and save again");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Stored as version 2.")).toBeInTheDocument();
    const [first, second] = publishCalls();
    expect(first.headers["Idempotency-Key"]).toBe(second.headers["Idempotency-Key"]);
    await user.type(screen.getByLabelText("Rate"), "1.3");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await waitFor(() => expect(publishCalls()).toHaveLength(3));
    expect(publishCalls()[2].headers["Idempotency-Key"]).not.toBe(first.headers["Idempotency-Key"]);
  });

  it("reports a version conflict on the confirm path and a changed-body replay as a plain failure", async () => {
    let status = 409;
    answer({ history: json(200, { entries: [history("1")] }) });
    const base = server.handler;
    server.handler = (url, method) =>
      method === "POST"
        ? status === 409
          ? json(409, { code: "FX_RATE_CONFLICT", detail: "a concurrent write already created this rate version" })
          : json(422, { code: "COM-0409", detail: "idempotency key reused with a different request" })
        : base(url, method);
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.5");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await user.click(await screen.findByRole("button", { name: "Confirm and save" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Someone else saved this rate first");
    expect(screen.queryByRole("button", { name: "Confirm and save" })).not.toBeInTheDocument();
    status = 422;
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    await user.click(await screen.findByRole("button", { name: "Confirm and save" }));
    const failure = await screen.findByText("Could not save the rate");
    expect(failure.closest('[role="alert"]')?.textContent).toContain("idempotency key reused");
  });

  it("rejects the same currency on both sides and rates the database cannot hold", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.12345678901");
    await user.clear(screen.getByLabelText("To currency"));
    await user.type(screen.getByLabelText("To currency"), "EUR");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Choose a different currency from the first one.")).toBeInTheDocument();
    expect(screen.getByText("Use at most 10 decimal places.")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("From currency"));
    await user.type(screen.getByLabelText("From currency"), "GBP");
    expect(screen.queryByText("Choose a different currency from the first one.")).not.toBeInTheDocument();
    expect(screen.getByText("Use at most 10 decimal places.")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Rate"));
    await user.type(screen.getByLabelText("Rate"), "12345678901");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    expect(await screen.findByText("Use at most 10 digits before the decimal point.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("keeps a format error on the to currency when the from currency is edited", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await fillEntry(user, "1.2");
    await user.clear(screen.getByLabelText("To currency"));
    await user.type(screen.getByLabelText("To currency"), "US");
    await user.click(screen.getByRole("button", { name: "Save rate" }));
    const toError = validateEntry({ ...emptyEntry, to: "US" }).to as string;
    expect(await screen.findByText(toError)).toBeInTheDocument();
    await user.clear(screen.getByLabelText("From currency"));
    await user.type(screen.getByLabelText("From currency"), "GBP");
    expect(screen.getByText(toError)).toBeInTheDocument();
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
    expect(screen.getByText(/fx.rate.view/)).toBeInTheDocument();
  });

  it("shows a generic error when the list fails", async () => {
    answer({ list: json(500, { code: "COM-0500", detail: "internal error" }) });
    renderAt("/fx/rate-types");
    expect(await screen.findByText("Could not load the rate types")).toBeInTheDocument();
  });

  it("creates a rate type", async () => {
    answer({ create: json(201, { id: "t3", code: "BUDGET", name: "Budget", allowInverse: false, provider: "csv" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByRole("button", { name: "CLOSING" });
    await user.type(await screen.findByLabelText("Code"), "budget");
    await user.type(screen.getByLabelText("Name"), "Budget");
    await user.type(screen.getByLabelText("Provider"), "csv");
    await user.click(screen.getByLabelText("Allow inverse rates"));
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect(await screen.findByText("Rate type BUDGET saved")).toBeInTheDocument();
    const create = rateTypeCalls("POST");
    expect(create).toHaveLength(1);
    expect(create[0]?.url.pathname).toBe("/api/v1/fx/rate-types");
    expect(create[0]?.body).toEqual({ code: "BUDGET", name: "Budget", allowInverse: false, provider: "csv" });
    expect(rateTypeCalls("PUT")).toHaveLength(0);
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
    expect(rateTypeCalls("POST")).toHaveLength(0);
  });

  it("explains a missing permission to save", async () => {
    answer({ create: json(403, { code: "COM-0403", detail: "forbidden" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByRole("button", { name: "CLOSING" });
    await user.type(await screen.findByLabelText("Code"), "X");
    await user.type(screen.getByLabelText("Name"), "Xtra");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    const alert = await screen.findByText("You do not have permission");
    expect(alert).toBeInTheDocument();
    expect(screen.getByText(/fx.rate.maintain/)).toBeInTheDocument();
  });

  it("shows the server's rejection when no field owns it", async () => {
    answer({ create: json(400, { code: "FX_RATE_INVALID", detail: "rate type code and name are required" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByRole("button", { name: "CLOSING" });
    await user.type(await screen.findByLabelText("Code"), "X");
    await user.type(screen.getByLabelText("Name"), "Xtra");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect((await screen.findByRole("alert")).textContent).toContain("rate type code and name are required");
  });

  it("shows the code error when the server reports the code was taken meanwhile", async () => {
    answer({ create: json(409, { code: "FX_RATE_TYPE_EXISTS", detail: "a rate type with this code already exists" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByRole("button", { name: "CLOSING" });
    await user.type(screen.getByLabelText("Code"), "budget");
    await user.type(screen.getByLabelText("Name"), "Budget");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect(await screen.findByText("A rate type with this code already exists. Open it in the table to edit it.")).toBeInTheDocument();
    expect(screen.queryByText("Someone else saved this rate first")).not.toBeInTheDocument();
    expect(rateTypeCalls("POST")).toHaveLength(1);
    expect(rateTypeCalls("PUT")).toHaveLength(0);
  });

  it("refuses to create a code that already exists instead of overwriting it", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByRole("button", { name: "CLOSING" });
    await user.type(screen.getByLabelText("Code"), "closing");
    await user.type(screen.getByLabelText("Name"), "Other");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect(await screen.findByText("A rate type with this code already exists. Open it in the table to edit it.")).toBeInTheDocument();
    expect(screen.getByLabelText("Code")).toHaveFocus();
    expect(rateTypeCalls("POST")).toHaveLength(0);
  });

  it("does not create a rate type while the existing codes could not be loaded", async () => {
    answer({
      list: json(500, { code: "COM-0500", detail: "internal error" }),
      create: json(201, {
        id: "t9",
        code: "CLOSING",
        name: "Other",
        allowInverse: true,
      }),
    });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByText("Could not load the rate types");
    await user.type(screen.getByLabelText("Code"), "closing");
    await user.type(screen.getByLabelText("Name"), "Other");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect(await screen.findByText(/could not be checked yet/)).toBeInTheDocument();
    expect(screen.getByLabelText("Code")).toHaveFocus();
    expect(rateTypeCalls("POST")).toHaveLength(0);
  });

  it("clears the unavailable-list field error after the list recovers", async () => {
    answer({ list: json(500, { code: "COM-0500", detail: "internal error" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByText("Could not load the rate types");
    await user.type(screen.getByLabelText("Code"), "BUDGET");
    await user.type(screen.getByLabelText("Name"), "Budget");
    await user.click(screen.getByRole("button", { name: "Create rate type" }));
    expect(await screen.findByText(/could not be checked yet/)).toBeInTheDocument();
    answer({});
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("button", { name: "AVERAGE" });
    await waitFor(() => expect(screen.queryByText(/could not be checked yet/)).not.toBeInTheDocument());
    expect(rateTypeCalls("POST")).toHaveLength(0);
  });

  it("keeps the edit form while saving and disables New rate type", async () => {
    answer({ put: json(200, { ...average, name: "Average updated" }) });
    const release = holdRequests("PUT");
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "Average updated");
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    const newButton = screen.getByRole("button", { name: "New rate type" });
    await waitFor(() => expect(newButton).toBeDisabled());
    expect(screen.getByLabelText("Code")).toHaveValue("AVERAGE");
    expect(screen.getByLabelText("Name")).toHaveValue("Average updated");
    release();
    expect(await screen.findByText("Rate type AVERAGE saved")).toBeInTheDocument();
  });

  it("locks the form and ignores opening rows while a save is in flight", async () => {
    answer({ put: json(200, { ...average, name: "Average" }) });
    const release = holdRequests("PUT");
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(screen.getByLabelText("Name")).toBeDisabled());
    expect(screen.getByLabelText("Provider")).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "CLOSING" }));
    expect(screen.getByLabelText("Name")).toHaveValue("Monthly average");
    release();
    expect(await screen.findByText("Rate type AVERAGE saved")).toBeInTheDocument();
    expect(rateTypeCalls("PUT")).toHaveLength(1);
    expect(screen.getByLabelText("Name")).toBeEnabled();
  });

  it("clears a field's error as soon as it is edited, without moving focus", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "Create rate type" }));
    await screen.findByText("Enter a name, for example Month-end closing.");
    await user.type(screen.getByLabelText("Name"), "N");
    expect(screen.queryByText("Enter a name, for example Month-end closing.")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Name")).not.toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Name")).toHaveFocus();
    expect(screen.getByLabelText("Code")).toHaveAttribute("aria-invalid", "true");
  });

  it("moves focus to the name when a row is opened", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "CLOSING" }));
    expect(screen.getByLabelText("Name")).toHaveFocus();
  });

  it("offers Try again when the list fails and recovers", async () => {
    answer({ list: json(500, { code: "COM-0500", detail: "internal error" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await screen.findByText("Could not load the rate types");
    answer({});
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "AVERAGE" })).toBeInTheDocument();
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

const importRun = (extra: Record<string, unknown> = {}) =>
  json(200, { runId: "r1", provider: "ecb", rateType: "AVERAGE", date: "2026-03-02", attempts: 1, imported: 30, unchanged: 2, alreadyDone: false, degraded: false, ...extra });
const importCalls = () => server.calls.filter((c) => c.url.pathname.endsWith(":import"));

describe("import now", () => {
  it("is offered only for a rate type that has a provider", async () => {
    answer({});
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "CLOSING" }));
    expect(screen.queryByRole("button", { name: "Import now" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "AVERAGE" }));
    expect(await screen.findByRole("button", { name: "Import now" })).toBeInTheDocument();
  });

  it("is not offered for a provider the server cannot import from", async () => {
    answer({ list: json(200, [{ id: "t3", code: "BUDGET", name: "Budget", allowInverse: false, provider: "csv" }]) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "BUDGET" }));
    expect(screen.queryByRole("button", { name: "Import now" })).not.toBeInTheDocument();
  });

  it("imports through the provider route, locks the button while pending and reports the summary", async () => {
    answer({ importRun: importRun() });
    const release = holdRequests("POST");
    const user = userEvent.setup();
    const { container } = renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    const button = await screen.findByRole("button", { name: "Import now" });
    await user.click(button);
    await waitFor(() => expect(button).toHaveAttribute("aria-busy", "true"));
    await user.click(button);
    release();
    expect(await screen.findByText("Import finished")).toBeInTheDocument();
    expect(screen.getByText(/30 rates stored and 2 unchanged from ecb dated 2026-03-02/)).toBeInTheDocument();
    expect(importCalls()).toHaveLength(1);
    expect(importCalls()[0].body).toEqual({ rateType: "AVERAGE" });
    expect(button).not.toHaveAttribute("aria-busy");
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports the cube date and asks to retry when the provider has not published today", async () => {
    answer({ importRun: importRun({ pending: true, date: "2026-02-27" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.click(await screen.findByRole("button", { name: "Import now" }));
    expect(await screen.findByText("Latest rates stored")).toBeInTheDocument();
    expect(screen.getByText(/rates dated 2026-02-27 were stored.*import again later/)).toBeInTheDocument();
  });

  it("says when the day was already imported", async () => {
    answer({ importRun: importRun({ alreadyDone: true }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.click(await screen.findByRole("button", { name: "Import now" }));
    expect(await screen.findByText("Already imported")).toBeInTheDocument();
  });

  it("says the import is still running on a 409 instead of reporting a failure", async () => {
    answer({ importRun: json(409, { code: "FX_IMPORT_IN_PROGRESS", detail: "an import is still running" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.click(await screen.findByRole("button", { name: "Import now" }));
    expect(await screen.findByText("Import still running")).toBeInTheDocument();
    expect(screen.getByText(/Try again shortly/)).toBeInTheDocument();
    expect(screen.queryByText("Could not import the rates")).not.toBeInTheDocument();
    expect(screen.queryByText("Already imported")).not.toBeInTheDocument();
  });

  it("warns when the provider is down and last-known rates stay in use", async () => {
    answer({ importRun: importRun({ degraded: true, lastSuccessAt: "2026-03-01T09:00:00Z" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.click(await screen.findByRole("button", { name: "Import now" }));
    expect(await screen.findByText("The provider is unavailable")).toBeInTheDocument();
    expect(screen.getByText(/from 2026-03-01T09:00:00Z/)).toBeInTheDocument();
  });

  it("names the fx.rate.administer duty on a 403", async () => {
    answer({ importRun: json(403, { code: "AUTH-0003", detail: "duty missing" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.click(await screen.findByRole("button", { name: "Import now" }));
    expect(await screen.findByText(/fx.rate.administer/)).toBeInTheDocument();
    expect(screen.queryByText("Import finished")).not.toBeInTheDocument();
  });

  it("shows the server message when the import fails", async () => {
    answer({ importRun: json(502, { code: "FX_PROVIDER_FAILED", detail: "provider outage and no last-known rate" }) });
    const user = userEvent.setup();
    renderAt("/fx/rate-types");
    await user.click(await screen.findByRole("button", { name: "AVERAGE" }));
    await user.click(await screen.findByRole("button", { name: "Import now" }));
    expect(await screen.findByText("Could not import the rates")).toBeInTheDocument();
    expect(screen.getByText("provider outage and no last-known rate")).toBeInTheDocument();
  });

  it("is also offered on the rate entry screen once a provider rate type is picked", async () => {
    answer({ importRun: importRun() });
    const user = userEvent.setup();
    renderAt("/fx/rate-entry");
    await screen.findByRole("option", { name: "AVERAGE – Monthly average" });
    expect(screen.queryByRole("button", { name: "Import now" })).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Rate type"), "AVERAGE");
    await user.click(await screen.findByRole("button", { name: "Import now" }));
    expect(await screen.findByText("Import finished")).toBeInTheDocument();
  });
});
