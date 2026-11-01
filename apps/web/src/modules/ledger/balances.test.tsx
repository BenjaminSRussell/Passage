import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { fetchTrialBalance } from "./api/client";
import { parseDimensionFilter } from "./BatchLinesEditor";
import { parseGroupBy, transactionsLink } from "./pages";
import { json, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";

const grouped = {
  ledgerId: LEDGER,
  currency: "USD",
  rows: [
    { account: "6010", name: "Freight", debit: "140.00", credit: "0.00", net: "140.00", lineCount: 2, dimensions: { DEPT: "D100", CC: "" } },
    { account: "6010", name: "Freight", debit: "7.00", credit: "0.00", net: "7.00", lineCount: 1, dimensions: { DEPT: "D200", CC: "C1" } },
  ],
  debit: "147.00",
  credit: "147.00",
  net: "0.00",
  balanced: true,
  groupBy: ["DEPT", "CC"],
};

describe("balance dimension inquiry", () => {
  it("parses the group-by attributes", () => {
    expect(parseGroupBy("")).toEqual([]);
    expect(parseGroupBy("DEPT, CC")).toEqual(["DEPT", "CC"]);
    expect(parseGroupBy("dept")).toBeNull();
    expect(parseGroupBy("DEPT, DEPT")).toBeNull();
    expect(parseGroupBy("A,B,C,D,E,F")).toBeNull();
  });

  it("sends the dimension filter and grouping as query parameters", async () => {
    let seen: URL | undefined;
    const fetchImpl = async (input: string) => {
      seen = new URL(input, "http://localhost");
      return new Response(JSON.stringify(grouped), { status: 200 });
    };
    await fetchTrialBalance({ ledgerId: LEDGER, dimensions: { DEPT: "D100" }, groupBy: ["DEPT", "CC"] }, fetchImpl);
    expect(seen?.searchParams.get("dim.DEPT")).toBe("D100");
    expect(seen?.searchParams.get("group_by")).toBe("DEPT,CC");
  });

  it("carries the filter and the row's group values into the transactions link", () => {
    const link = new URL(transactionsLink({ ledgerId: LEDGER, periodFrom: "2026-01", periodTo: "2026-02", dimensions: { DEPT: "D200" } }, "6010", { DEPT: "D200", CC: "" }), "http://localhost");
    // An empty group value is the "(none)" row, so the lines must not set the attribute either.
    expect(link.searchParams.get("dims")).toBe("CC=, DEPT=D200");
    expect(link.searchParams.get("date_from")).toBe("2026-01-01");
    expect(link.searchParams.get("date_to")).toBe("2026-02-28");
    expect(link.searchParams.get("account_from")).toBe("6010");
    expect(link.searchParams.get("account_to")).toBe("6010");
  });

  it("parses a filter whose empty value means the attribute is not set", () => {
    expect(parseDimensionFilter("DEPT=D100, CC=")).toEqual({ DEPT: "D100", CC: "" });
    expect(parseDimensionFilter("DEPT")).toBeNull();
    expect(parseDimensionFilter("DEPT=A, DEPT=B")).toBeNull();
  });

  it("opens the lines of a (none) row with an empty dimension value", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/balances") return json(200, grouped);
      if (url.pathname === "/api/v1/ledger/journal-lines") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/ledger/trial-balance");
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText(/Group by dimensions/), { target: { value: "DEPT, CC" } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    const table = await screen.findByRole("table", { name: "Trial balance" });
    fireEvent.click(await within(table).findByText("(none)"));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === "/api/v1/ledger/journal-lines")).toBe(true));
    const call = server.calls.find((c) => c.url.pathname === "/api/v1/ledger/journal-lines");
    expect(call?.url.searchParams.get("dim.DEPT")).toBe("D100");
    expect(call?.url.searchParams.has("dim.CC")).toBe(true);
    expect(call?.url.searchParams.get("dim.CC")).toBe("");
  });

  it("names the field the service refused, such as an attribute the ledger does not define", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/ledger/balances"
        ? json(400, { code: "COM-0001", detail: "request failed validation", errors: { group_by: "PROJECT is not a dimension attribute of the ledger" } })
        : undefined;
    renderAt("/ledger/trial-balance");
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText(/Group by dimensions/), { target: { value: "PROJECT" } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    expect((await findAlert()).textContent).toContain("group_by PROJECT is not a dimension attribute of the ledger");
  });

  it("shows a column per grouped attribute and opens the row's lines", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/balances") return json(200, grouped);
      if (url.pathname === "/api/v1/ledger/journal-lines") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/ledger/trial-balance");
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText(/Group by dimensions/), { target: { value: "DEPT, CC" } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    const table = await screen.findByRole("table", { name: "Trial balance" });
    expect(within(table).getByRole("columnheader", { name: "DEPT" })).toBeInTheDocument();
    expect(await within(table).findByText("D200")).toBeInTheDocument();
    expect(within(table).getByText("(none)")).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname === "/api/v1/ledger/balances")?.url.searchParams.get("group_by")).toBe("DEPT,CC");
    fireEvent.click(within(table).getByText("D200"));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === "/api/v1/ledger/journal-lines")).toBe(true));
    const call = server.calls.find((c) => c.url.pathname === "/api/v1/ledger/journal-lines");
    expect(call?.url.searchParams.get("dim.CC")).toBe("C1");
    expect(call?.url.searchParams.get("dim.DEPT")).toBe("D200");
  });

  it("marks a filtered balance as not subject to the balanced check and rejects bad input", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/ledger/balances"
        ? json(200, { ...grouped, rows: [grouped.rows[0]], groupBy: undefined, balanced: false, filtered: true })
        : undefined;
    renderAt("/ledger/trial-balance");
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText(/Dimension filter/), { target: { value: "dept=x" } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    expect(await screen.findByText(/ATTRIBUTE=value pairs/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Dimension filter/), { target: { value: "DEPT=D100" } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    expect(await screen.findByTestId("tb-balanced")).toHaveTextContent("Not applicable to a dimension filter");
  });

  it("reports a failed load with a retry", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/ledger/balances" ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : undefined);
    renderAt("/ledger/trial-balance");
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    expect((await findAlert()).textContent).toContain("ledger not found");
  });
});
