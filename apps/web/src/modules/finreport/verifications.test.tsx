import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findAlert, findTable } from "../../test-utils";
import type { Verification } from "./api/client";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190f0a0-0000-7000-8000-000000000201";
const ID = "0190f0a0-0000-7000-8000-000000000301";

const differs: Verification = {
  id: ID,
  ledgerId: LEDGER,
  status: "differs",
  hwmBefore: 9,
  hwmAfter: 9,
  entriesPending: 0,
  accountsCompared: 3,
  diffCount: 2,
  diffs: [
    { account: "1000", readModel: "60.000000", ledger: "61.500000", difference: "-1.500000" },
    { account: "5000", readModel: "0.000000", ledger: "1.500000", difference: "-1.500000" },
  ],
  requestedBy: "u1",
  verifiedAt: "2026-04-02T01:00:00Z",
};
const matched: Verification = { ...differs, id: "0190f0a0-0000-7000-8000-000000000302", status: "matched", diffCount: 0, diffs: [], verifiedAt: "2026-04-01T01:00:00Z" };
const moved: Verification = { ...differs, id: "0190f0a0-0000-7000-8000-000000000303", status: "inconclusive", hwmAfter: 11, verifiedAt: "2026-04-03T01:00:00Z" };

describe("VerificationsPage", () => {
  it("lists past checks with their result and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/balance-verifications") return json(200, { items: [differs, matched, moved] });
      if (url.pathname === `/api/v1/reporting/balance-verifications/${ID}`) return json(200, differs);
      return undefined;
    };
    const { container } = renderAt("/finreport/verifications");
    const table = await findTable({ name: "Ledger checks" });
    expect(await within(table).findByText("Differs")).toBeInTheDocument();
    expect(within(table).getByText("Matched")).toBeInTheDocument();
    expect(within(table).getByText("Inconclusive")).toBeInTheDocument();
    expect(within(table).getByText("9 to 11")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: differs.verifiedAt }));
    expect(await screen.findByRole("heading", { name: "Ledger check" })).toBeInTheDocument();
  });

  it("shows the empty state and the load error with a retry", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/reporting/balance-verifications" ? json(200, { items: [] }) : undefined);
    renderAt("/finreport/verifications");
    expect(await screen.findByText("No ledger has been checked yet")).toBeInTheDocument();
  });

  it("reports a failed load", async () => {
    server.handler = () => problem(500, "COM-0500", "boom");
    renderAt("/finreport/verifications");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("boom");
  });

  it("refuses a ledger id that is not a UUID without calling the service", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/reporting/balance-verifications" ? json(200, { items: [] }) : undefined);
    renderAt("/finreport/verifications");
    await screen.findByText("No ledger has been checked yet");
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: "ledger-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Check ledger" }));
    expect(await screen.findByText(/Choose the ledger/)).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("checks a ledger with an Idempotency-Key and opens the stored check", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/reporting/balance-verifications") return json(201, differs);
      if (url.pathname === "/api/v1/reporting/balance-verifications") return json(200, { items: [] });
      if (url.pathname === `/api/v1/reporting/balance-verifications/${ID}`) return json(200, differs);
      return undefined;
    };
    renderAt("/finreport/verifications");
    await screen.findByText("No ledger has been checked yet");
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: ` ${LEDGER.toUpperCase()} ` } });
    fireEvent.click(screen.getByRole("button", { name: "Check ledger" }));
    expect(await screen.findByRole("heading", { name: "Ledger check" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ ledgerId: LEDGER });
    expect(header(post!, "Idempotency-Key")).toBeTruthy();
  });

  it("keeps the same Idempotency-Key when the click is retried after a failure", async () => {
    let fail = true;
    server.handler = (url, init) => {
      if (init.method === "POST") return fail ? problem(502, "REPORT-0036", "the ledger trial balance could not be read") : json(200, differs);
      if (url.pathname === "/api/v1/reporting/balance-verifications") return json(200, { items: [] });
      if (url.pathname === `/api/v1/reporting/balance-verifications/${ID}`) return json(200, differs);
      return undefined;
    };
    renderAt("/finreport/verifications");
    await screen.findByText("No ledger has been checked yet");
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Check ledger" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("REPORT-0036");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Check ledger" }));
    await screen.findByRole("heading", { name: "Ledger check" });
    const keys = server.calls.filter((c) => c.method === "POST").map((c) => header(c, "Idempotency-Key"));
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);
  });
});

describe("VerificationPage", () => {
  it("shows the differing accounts of a check", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/reporting/balance-verifications/${ID}` ? json(200, differs) : undefined);
    const { container } = renderAt(`/finreport/verifications/${ID}`);
    const table = await findTable({ name: "Differing accounts" });
    expect(within(table).getByText("61.500000")).toBeInTheDocument();
    expect(within(table).getAllByText("-1.500000")).toHaveLength(2);
    expect(screen.getByText(/2 of 3 accounts differ/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when every account matches and when the check was inconclusive", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/reporting/balance-verifications/${matched.id}` ? json(200, matched) : undefined);
    const first = renderAt(`/finreport/verifications/${matched.id}`);
    expect(await screen.findByText("All 3 accounts have the same net in the read model and in the ledger.")).toBeInTheDocument();
    expect(await screen.findByText("No account differs")).toBeInTheDocument();
    first.unmount();
    server.handler = (url) => (url.pathname === `/api/v1/reporting/balance-verifications/${moved.id}` ? json(200, moved) : undefined);
    renderAt(`/finreport/verifications/${moved.id}`);
    expect(await screen.findByText(/while the ledger was read/)).toBeInTheDocument();
  });

  it("explains a check left inconclusive by entries waiting ahead of the mark", async () => {
    const waiting: Verification = { ...differs, status: "inconclusive", entriesPending: 2 };
    server.handler = () => json(200, waiting);
    renderAt(`/finreport/verifications/${ID}`);
    expect(await screen.findByText(/2 entries were waiting ahead of sequence 9/)).toBeInTheDocument();
  });

  it("withholds the ledger amounts from a user who may not read ledger balances", async () => {
    server.handler = () => json(200, { ...differs, diffs: [], diffsWithheld: true });
    const { container } = renderAt(`/finreport/verifications/${ID}`);
    expect(await screen.findByText("Ledger amounts withheld")).toBeInTheDocument();
    expect(screen.getByText(/2 accounts differ\./)).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Differing accounts" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Showing the first/)).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("notes when only the first accounts are shown", async () => {
    server.handler = () => json(200, { ...differs, diffCount: 900 });
    renderAt(`/finreport/verifications/${ID}`);
    expect(await screen.findByText("Showing the first 2 of 900 differing accounts.")).toBeInTheDocument();
  });

  it("explains a check that does not exist", async () => {
    server.handler = () => problem(404, "REPORT-0035", "balance verification x does not exist");
    renderAt(`/finreport/verifications/${ID}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("REPORT-0035");
    await waitFor(() => expect(screen.getByRole("link", { name: "Back to ledger verification" })).toBeInTheDocument());
  });
});
