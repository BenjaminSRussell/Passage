import axe from "axe-core";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lookupProblem, problemCatalog } from "./catalog";
import { normalizeProblem, problemSummary } from "./problem";
import { ProblemMessage } from "./ProblemMessage";

class FakeApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

describe("ProblemMessage", () => {
  it("shows the catalog message and what to do, with the code, server detail and request id under Details", async () => {
    const user = userEvent.setup();
    render(<ProblemMessage problem={{ code: "PRC-PO-009", detail: "stale", requestId: "req-42" }} />);
    expect(screen.getByText("Someone else changed this purchase order after you opened it. Reload the page to see the latest version, then make your change again.")).toBeInTheDocument();
    const summary = screen.getByText("Details");
    const details = summary.closest("details") as HTMLElement;
    expect(details).not.toHaveAttribute("open");
    await user.click(summary);
    expect(details).toHaveAttribute("open");
    expect(within(details).getByText("PRC-PO-009")).toBeVisible();
    expect(within(details).getByText("stale")).toBeVisible();
    expect(within(details).getByText("req-42")).toBeVisible();
  });

  it("falls back to the server detail for an unknown code and keeps the code in Details", () => {
    render(<ProblemMessage problem={new FakeApiError(422, "XYZ-0001", "The widget is out of range.")} />);
    expect(screen.getByText("The widget is out of range.")).toBeInTheDocument();
    expect(screen.getByText("XYZ-0001")).toBeInTheDocument();
    expect(screen.queryByText("Message from the service")).not.toBeInTheDocument();
  });

  it("falls back to the server title when there is no detail", () => {
    render(<ProblemMessage problem={{ problem: { title: "Widget conflict", status: 409, code: "XYZ-0409" } }} />);
    expect(screen.getByText("Widget conflict")).toBeInTheDocument();
    expect(screen.getByText("XYZ-0409")).toBeInTheDocument();
  });

  it("shows a plain error or string without a Details disclosure", () => {
    const { rerender } = render(<ProblemMessage problem={new Error("The network is down.")} />);
    expect(screen.getByText("The network is down.")).toBeInTheDocument();
    expect(screen.queryByText("Details")).not.toBeInTheDocument();
    rerender(<ProblemMessage problem="Nothing was saved." />);
    expect(screen.getByText("Nothing was saved.")).toBeInTheDocument();
    rerender(<ProblemMessage problem={null} fallback="The request failed." />);
    expect(screen.getByText("The request failed.")).toBeInTheDocument();
  });

  it("keeps the specific server detail reachable under Details when the catalog message is shown", async () => {
    const user = userEvent.setup();
    render(<ProblemMessage problem={{ code: "SLS-0058", detail: "order SO-1042 is in status DRAFT", requestId: "req-9" }} />);
    expect(screen.getByText(/The order can't move to the next step in its current state/)).toBeInTheDocument();
    await user.click(screen.getByText("Details"));
    expect(screen.getByText("order SO-1042 is in status DRAFT")).toBeVisible();
  });

  it("has no accessibility violations, collapsed or open", async () => {
    const user = userEvent.setup();
    const { container } = render(<ProblemMessage problem={{ code: "PRC-PO-009", detail: "stale", requestId: "req-42" }} />);
    const rules = { rules: { "color-contrast": { enabled: false } } };
    expect((await axe.run(container, rules)).violations).toEqual([]);
    await user.click(screen.getByText("Details"));
    expect((await axe.run(container, rules)).violations).toEqual([]);
  });

  it("uses module wording when given, and still keeps the code", () => {
    render(<ProblemMessage problem={{ code: "HCM-LOAN-001", message: "loans open" }} message="Items are still out on loan." />);
    expect(screen.getByText("Items are still out on loan.")).toBeInTheDocument();
    expect(screen.getByText("HCM-LOAN-001")).toBeInTheDocument();
    expect(screen.getByText("loans open")).toBeInTheDocument();
  });
});

describe("problem catalog", () => {
  const entries = Object.entries(problemCatalog);

  it("covers at least 100 codes", () => {
    expect(entries.length).toBeGreaterThanOrEqual(100);
  });

  it.each(["COM-0001", "COM-0500", "AUTH-0003", "PRC-PO-009", "RCV-OVR-001", "AP-QTY-001", "SLS-0006", "INV-NEG-001", "LEDGER_UNBALANCED"])("has an entry for %s", (code) => {
    expect(lookupProblem(code)).toBeDefined();
  });

  it("writes every entry as plain sentences that never repeat a code", () => {
    for (const [code, entry] of entries) {
      for (const text of [entry.message, entry.action]) {
        expect(text, code).toMatch(/^[A-Z].*[.]$/s);
        expect(text, code).not.toMatch(/\b[A-Z]{2,}(?:[-_][A-Z0-9]+)*[-_]\d{3,4}\b/);
        expect(text, code).not.toMatch(/\b(?:FR|BR|AC)-\d+\.\d+/);
      }
    }
  });

  it("does not inherit object prototype keys", () => {
    expect(lookupProblem("toString")).toBeUndefined();
    expect(lookupProblem(undefined)).toBeUndefined();
  });
});

describe("normalizeProblem and problemSummary", () => {
  it("reads codes and text from module error shapes", () => {
    expect(normalizeProblem({ message: "boom", code: "COM-0500", status: 500 })).toEqual({ code: "COM-0500", detail: "boom", status: 500, title: undefined, requestId: undefined });
    expect(normalizeProblem({ problem: { code: "PRC-PO-007", detail: "below received", traceId: "t-1" } })).toMatchObject({ code: "PRC-PO-007", detail: "below received", requestId: "t-1" });
    expect(normalizeProblem("text")).toEqual({ detail: "text" });
    expect(normalizeProblem(undefined)).toEqual({});
  });

  it("summarises a known code from the catalog and an unknown one from the server", () => {
    expect(problemSummary({ code: "AUTH-0003", message: "missing duty purchasing.po.write" })).toBe(
      "You don't have the duty or the legal-entity access this action needs. Ask an administrator for access if you need it.",
    );
    expect(problemSummary({ code: "XYZ-1", message: "Server words." })).toBe("Server words.");
    expect(problemSummary(null, "Fallback.")).toBe("Fallback.");
  });
});

const goSources = import.meta.glob(
  [
    "../../../../../services/{fulfillment,billing,receivables,pricing,purchasing,receiving,payables,sales,inventory,party,ledger,tax,product}/**/*.go",
    "!../../../../../services/**/*_test.go",
  ],
  { query: "?raw", import: "default", eager: true },
) as Record<string, string>;

const orderToCashCodePatterns: Record<string, RegExp> = {
  fulfillment: /^FUL-\d{4}$/,
  billing: /^BIL-\d{4}$/,
  receivables: /^AR-\d{4}$/,
  pricing: /^PRC-\d{4}$/,
  purchasing: /^PRC-[A-Z]+-\d{3}$/,
  receiving: /^(?:RCV-[A-Z]+-\d{3}|INV-SER-\d{3}|PER-AP-\d{3})$/,
  payables: /^(?:AP-[A-Z]+-\d{3}|PAY-\d{4})$/,
  sales: /^SLS-\d{4}$/,
  inventory: /^INV-[A-Z]+-\d{3}$/,
  party: /^PTY-[A-Z0-9-]+$/,
  ledger: /^LEDGER_(?!IDEMPOTENCY_TTL_DAYS$)[A-Z_]+$/,
  tax: /^TAX-\d{4}$/,
  product: /^PRD-[A-Z0-9-]+$/,
};

describe("procure-to-pay and order-to-cash problem codes", () => {
  const found = new Map<string, string>();
  for (const [path, source] of Object.entries(goSources)) {
    const service = /\/services\/([^/]+)\//.exec(path)?.[1] ?? "";
    for (const match of source.matchAll(/"([A-Z][A-Z0-9_-]+?)(?::|")/g)) {
      if (orderToCashCodePatterns[service]?.test(match[1])) found.set(match[1], service);
    }
  }

  it("reads the Go sources of every procure-to-pay and order-to-cash service", () => {
    const services = new Set(found.values());
    expect([...services].sort()).toEqual(Object.keys(orderToCashCodePatterns).sort());
    expect(found.size).toBeGreaterThan(300);
  }, 60_000);

  it("has a catalog message for every code those services can return", () => {
    const missing = [...found.keys()].filter((code) => !lookupProblem(code)).sort();
    expect(missing).toEqual([]);
  }, 60_000);
});
