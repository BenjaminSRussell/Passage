import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ChatResponse } from "./api/client";
import { pageViolations } from "./a11yTest";
import { CopilotPanel } from "./CopilotPanel";
import { canInsertDraft, GENERATED_BADGE, isCopilotMode, toolCallLabel } from "./domain";

const answer: ChatResponse = {
  answer: "Revenue was 1,200.00 USD in October, against 1,050.00 in September.",
  citations: [{ sourceType: "METRIC", sourceRef: "revenue", label: "Revenue, 2026-10 vs 2026-09" }],
  toolCalls: [{ tool: "passage.ledger.journal.get", status: "OK" }],
  refusal: null,
  generated: true,
};

describe("copilot domain rules", () => {
  it("inserts drafts only in draft mode and never after a refusal", () => {
    expect(canInsertDraft("draft", false)).toBe(true);
    expect(canInsertDraft("draft", true)).toBe(false);
    expect(canInsertDraft("ask", false)).toBe(false);
  });

  it("recognises the four modes and labels tool calls with their code", () => {
    expect(isCopilotMode("explain")).toBe(true);
    expect(isCopilotMode("delete")).toBe(false);
    expect(toolCallLabel({ tool: "passage.ap.invoice.post", status: "DENIED", code: "AGT-0203" })).toBe(
      "passage.ap.invoice.post: DENIED (AGT-0203)",
    );
  });
});

describe("CopilotPanel", () => {
  it("sends the mode and message, then shows a labelled answer with tool calls and citations", async () => {
    const ask = vi.fn(async () => answer);
    render(<CopilotPanel ask={ask} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "what was revenue last month vs the month before");
    await user.click(screen.getByRole("button", { name: "Send" }));

    expect(ask).toHaveBeenCalledWith({ mode: "ask", message: "what was revenue last month vs the month before" }, expect.any(Function));
    expect(await screen.findByRole("article", { name: "Copilot answer" })).toHaveTextContent(answer.answer);
    expect(screen.getByText(GENERATED_BADGE)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Tool calls" })).toHaveTextContent("passage.ledger.journal.get: OK");
    expect(screen.getByRole("list", { name: "Citations" })).toHaveTextContent("Revenue, 2026-10 vs 2026-09");
    expect(screen.queryByText("No answer yet")).toBeNull();
    expect(pageViolations()).toEqual([]);
  });

  it("switches mode with pressed state and sends the record context", async () => {
    const ask = vi.fn(async () => answer);
    render(<CopilotPanel ask={ask} recordRef="ledger.journal:JV-0042" />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Explain this record" }));
    expect(screen.getByRole("button", { name: "Explain this record" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Ask the data" })).toHaveAttribute("aria-pressed", "false");
    await user.type(screen.getByLabelText("Ask Copilot"), "why is this journal reversed");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(ask).toHaveBeenCalledWith({ mode: "explain", message: "why is this journal reversed", recordRef: "ledger.journal:JV-0042" }, expect.any(Function));
  });

  it("inserts a draft only when the user clicks Insert draft", async () => {
    const onInsertDraft = vi.fn();
    render(<CopilotPanel ask={async () => ({ ...answer, answer: "Dear customer, your invoice is overdue." })} onInsertDraft={onInsertDraft} initialMode="draft" />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "dunning letter for ACME");
    await user.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByRole("article", { name: "Copilot answer" });
    expect(onInsertDraft).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Insert draft" }));
    expect(onInsertDraft).toHaveBeenCalledWith("Dear customer, your invoice is overdue.");
  });

  it("shows a refusal with its code and offers no draft insert", async () => {
    const refusal: ChatResponse = {
      ...answer,
      answer: "",
      citations: [],
      toolCalls: [{ tool: "passage.ap.invoice.post", status: "DENIED", code: "AGT-0203" }],
      refusal: { code: "AGT-0203", message: "I cannot do that: you do not hold duty APInvoicePost required by passage.ap.invoice.post." },
    };
    render(<CopilotPanel ask={async () => refusal} onInsertDraft={vi.fn()} initialMode="draft" />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "post the invoice");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText(/do not hold duty APInvoicePost/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Insert draft" })).toBeNull();
  });

  it("reports an unavailable copilot as an alert", async () => {
    render(<CopilotPanel ask={async () => { throw new Error("Copilot is unavailable (HTTP 503)."); }} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "hello");
    await user.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("HTTP 503"));
  });

  it("shows the answer as it streams, labelled, then the final answer", async () => {
    let finish: (r: ChatResponse) => void = () => undefined;
    const ask = vi.fn(
      (_req: unknown, onToken?: (text: string) => void) =>
        new Promise<ChatResponse>((resolve) => {
          onToken?.("Revenue was ");
          onToken?.("1,200.00 USD");
          finish = resolve;
        }),
    );
    render(<CopilotPanel ask={ask} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "revenue last month");
    await user.click(screen.getByRole("button", { name: "Send" }));

    const live = await screen.findByRole("article", { name: "Copilot answer in progress" });
    expect(live).toHaveTextContent("Revenue was 1,200.00 USD");
    expect(live).toHaveTextContent(GENERATED_BADGE);
    expect(live).toHaveAttribute("aria-busy", "true");
    expect(screen.queryByText("No answer yet")).toBeNull();
    expect(pageViolations()).toEqual([]);

    finish(answer);
    expect(await screen.findByRole("article", { name: "Copilot answer" })).toHaveTextContent(answer.answer);
    expect(screen.queryByRole("article", { name: "Copilot answer in progress" })).toBeNull();
  });

  it("drops a partial answer when the stream fails, and shows the error", async () => {
    const ask = vi.fn(async (_req: unknown, onToken?: (text: string) => void) => {
      onToken?.("half an ans");
      throw new Error("AGT-0206: Provider unavailable.");
    });
    render(<CopilotPanel ask={ask} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "hello");
    await user.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("AGT-0206"));
    expect(screen.queryByText("half an ans")).toBeNull();
  });

  it("does not send an empty message", async () => {
    const ask = vi.fn(async () => answer);
    render(<CopilotPanel ask={ask} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(ask).not.toHaveBeenCalled();
  });
});
