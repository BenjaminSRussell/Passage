import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { createConversation as createConversationApi, type ChatResponse } from "./api/client";
import { askCopilot } from "./api/stream";
import { pageViolations } from "./a11yTest";
import { AskAnswer } from "./AskAnswer";
import { GENERATED_BADGE } from "./domain";
import { useCopilotConversation, type AskFn } from "./useCopilotConversation";

const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const ANSWER_HEADING_LEVEL = 3;
const QUESTION = "what is the total of our overdue invoices over $10k?";

const answer: ChatResponse = {
  answer: "Overdue invoices over $10k total 48,200.00 USD.",
  citations: [
    { sourceType: "RECORD", sourceRef: "record:ledger/ap-invoice/INV-1042", label: "Invoice INV-1042" },
    { sourceType: "DOC", sourceRef: "https://evil.example/steal", label: "External page" },
    { sourceType: "METRIC", sourceRef: "metric:overdue", label: "Overdue invoices" },
  ],
  toolCalls: [{ tool: "analytics.query", status: "OK" }],
  refusal: null,
  generated: true,
};

function Harness({ ask, createConversation, duties = [] }: { ask: AskFn; createConversation: () => Promise<string>; duties?: string[] }) {
  const conversation = useCopilotConversation({ ask, createConversation });
  return (
    <MemoryRouter>
      <PrincipalProvider duties={duties}>
        <button type="button" onClick={() => void conversation.send(QUESTION)}>
          Ask
        </button>
        <AskAnswer conversation={conversation} />
      </PrincipalProvider>
    </MemoryRouter>
  );
}

async function ask() {
  await userEvent.setup().click(screen.getByRole("button", { name: "Ask" }));
}

const newConversation = () => vi.fn(async () => CONVERSATION_ID);

describe("AskAnswer", () => {
  it("streams the answer in a busy article, then shows the badge, tools, sources and one completion announcement", async () => {
    let finish: (r: ChatResponse) => void = () => undefined;
    const send: AskFn = (_req, onToken) =>
      new Promise<ChatResponse>((resolve) => {
        onToken?.("Overdue invoices ");
        onToken?.("over $10k total");
        finish = resolve;
      });
    render(<Harness ask={send} createConversation={newConversation()} duties={["receiving.ap.inquire"]} />);
    await ask();

    const live = await screen.findByRole("article", { name: "Copilot answer in progress" });
    expect(live).toHaveAttribute("aria-busy", "true");
    expect(live).toHaveTextContent("Overdue invoices over $10k total");
    expect(screen.getByRole("status")).toBeEmptyDOMElement();

    finish(answer);
    const final = await screen.findByRole("article", { name: "Copilot answer" });
    expect(screen.queryByRole("article", { name: "Copilot answer in progress" })).toBeNull();
    expect(within(final).getByText(GENERATED_BADGE)).toBeInTheDocument();
    expect(final).toHaveTextContent(answer.answer);
    await userEvent.setup().click(within(final).getByRole("button", { name: "Data used" }));
    expect(within(final).getByRole("list", { name: "Tools called" })).toHaveTextContent("analytics.query: OK");
    expect(within(final).getByRole("list", { name: "Sources" })).toHaveTextContent("Invoice INV-1042");
    for (const name of ["Sources", "Tools called", "Records read"]) {
      expect(within(final).getByRole("heading", { name, level: 4 })).toBeInTheDocument();
    }
    const levels = screen.getAllByRole("heading").map((h) => Number(h.tagName.slice(1)));
    expect(levels[0]).toBe(ANSWER_HEADING_LEVEL);
    levels.forEach((level, i) => {
      expect(level - (levels[i - 1] ?? ANSWER_HEADING_LEVEL)).toBeLessThanOrEqual(1);
    });
    expect(screen.getByRole("status")).toHaveTextContent("The copilot has finished answering.");
    expect(pageViolations()).toEqual([]);
  });

  it("links only a record citation the user may open, and shows every other source as plain text", async () => {
    render(<Harness ask={async () => answer} createConversation={newConversation()} duties={["receiving.ap.inquire"]} />);
    await ask();
    const sources = within(await screen.findByRole("list", { name: "Sources" }));
    expect(sources.getAllByRole("link")).toHaveLength(1);
    expect(sources.getByRole("link", { name: "Invoice INV-1042" })).toHaveAttribute("href", "/ap-invoices/INV-1042");
    expect(sources.getByText("External page")).toBeInTheDocument();
    expect(sources.getByText("Overdue invoices")).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();
    expect(document.querySelector("a[href*='evil']")).toBeNull();
  });

  it("shows no link for a record the user holds no duty for", async () => {
    render(<Harness ask={async () => answer} createConversation={newConversation()} />);
    await ask();
    expect(within(await screen.findByRole("list", { name: "Sources" })).queryByRole("link")).toBeNull();
  });

  it("shows a refusal with its code and message, offers Try again, and no answer text", async () => {
    const refusal: ChatResponse = {
      ...answer,
      answer: "",
      citations: [],
      toolCalls: [{ tool: "depot.ap.invoice.get", status: "DENIED", code: "AGT-0203" }],
      refusal: { code: "AGT-0203", message: "I cannot show that: you do not hold the duty it needs." },
    };
    render(<Harness ask={async () => refusal} createConversation={newConversation()} />);
    await ask();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("AGT-0203");
    expect(alert).toHaveTextContent("you do not hold the duty it needs");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Sources" })).toBeNull();
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("alerts and offers Try again when the stream ends early, and Try again reuses the conversation and the question", async () => {
    const body = (text: string) => new Response(text, { status: 200, headers: { "content-type": "text/event-stream" } });
    const replies = [body('event: token\ndata: {"text":"half"}\n\n'), body(`event: done\ndata: ${JSON.stringify(answer)}\n\n`)];
    const fetchFn = vi.fn(async () => replies.shift() as Response) as unknown as typeof fetch;
    const send: AskFn = (req, onToken, onWithdrawn, _f, id) => askCopilot(req, onToken, onWithdrawn, fetchFn, id);
    const createConversation = newConversation();
    render(<Harness ask={send} createConversation={createConversation} />);
    await ask();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("ended before the answer was complete");
    expect(screen.queryByText("half")).toBeNull();
    expect(screen.queryByRole("article", { name: "Copilot answer in progress" })).toBeNull();

    await userEvent.setup().click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("article", { name: "Copilot answer" })).toHaveTextContent(answer.answer);
    expect(createConversation).toHaveBeenCalledTimes(1);
    const posted = (fetchFn as unknown as ReturnType<typeof vi.fn>).mock.calls.map((c) => [c[0], JSON.parse(String((c[1] as RequestInit).body))]);
    expect(posted).toEqual([
      [`/api/v1/agents/copilot/conversations/${CONVERSATION_ID}/messages`, { mode: "ask", message: QUESTION }],
      [`/api/v1/agents/copilot/conversations/${CONVERSATION_ID}/messages`, { mode: "ask", message: QUESTION }],
    ]);
  });

  it("rejects an answer that is not marked generated and shows an error, not an answer", async () => {
    const fetchFn = (async () => new Response(JSON.stringify({ ...answer, generated: false }), { status: 200, headers: { "content-type": "application/json" } })) as typeof fetch;
    const send: AskFn = (req, onToken, onWithdrawn, _f, id) => askCopilot(req, onToken, onWithdrawn, fetchFn, id);
    render(<Harness ask={send} createConversation={newConversation()} />);
    await ask();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByRole("article", { name: "Copilot answer" })).toBeNull();
  });

  it("shows the error and sends no message when the conversation cannot be created", async () => {
    const refused = (async () => new Response(JSON.stringify({ code: "AGT-0203" }), { status: 403 })) as typeof fetch;
    const send = vi.fn<AskFn>(async () => answer);
    render(<Harness ask={send} createConversation={() => createConversationApi(refused)} />);
    await ask();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("HTTP 403");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
    await waitFor(() => expect(send).not.toHaveBeenCalled());
  });

  it("keeps every turn as a labelled list item and reuses one conversation", async () => {
    const send = vi.fn<AskFn>(async () => answer);
    const create = newConversation();
    render(<Harness ask={send} createConversation={create} />);
    await ask();
    await screen.findByRole("article", { name: "Copilot answer" });
    await ask();
    await waitFor(() => expect(screen.getAllByRole("article", { name: "Copilot answer" })).toHaveLength(2));
    expect(within(screen.getByRole("list", { name: "Conversation so far" })).getAllByRole("heading", { level: 3 })).toHaveLength(2);
    expect(screen.getAllByRole("heading", { level: 3, name: `You asked: ${QUESTION}` })).toHaveLength(2);
    expect(create).toHaveBeenCalledTimes(1);
  });

  it("renders unverified badges and numbered citation links, and strips unmatched markers", async () => {
    const marked: ChatResponse = {
      ...answer,
      answer: "Total is 48,200 [unverified] per [[cite:metric:overdue]] and [[cite:nothing]] done.",
    };
    render(<Harness ask={async () => marked} createConversation={newConversation()} />);
    await ask();
    const final = await screen.findByRole("article", { name: "Copilot answer" });
    expect(within(final).getByText("Unverified")).toBeInTheDocument();
    expect(within(final).getByRole("link", { name: "Source 3" })).toHaveAttribute("href", expect.stringMatching(/-source-3$/));
    expect(final).not.toHaveTextContent("[[cite");
    expect(final).not.toHaveTextContent("[unverified]");
  });

  it.each([
    ["eight tool calls", { toolCalls: Array.from({ length: 8 }, () => ({ tool: "analytics.query", status: "OK" as const })) }],
    ["the limit message", { answer: "I could not complete the answer within the tool-call limit." }],
  ])("shows the may-be-partial note for %s", async (_name, patch) => {
    render(<Harness ask={async () => ({ ...answer, ...patch })} createConversation={newConversation()} />);
    await ask();
    expect(await screen.findByText(/may be partial/)).toBeInTheDocument();
  });

  it("shows no partial note below the limit", async () => {
    render(<Harness ask={async () => answer} createConversation={newConversation()} />);
    await ask();
    await screen.findByRole("article", { name: "Copilot answer" });
    expect(screen.queryByText(/may be partial/)).toBeNull();
  });

  it("announces the left-out notice once, in the polite region", async () => {
    const left: ChatResponse = { ...answer, excluded: { count: 2, reason: "NO_APPROVED_ROUTE" } };
    render(<Harness ask={async () => left} createConversation={newConversation()} />);
    await ask();
    await screen.findByRole("article", { name: "Copilot answer" });
    expect(screen.getByRole("status")).toHaveTextContent(/2/);
    expect(screen.getAllByRole("status")).toHaveLength(1);
  });

  it("shows the withdrawal code while streaming and then the refusal", async () => {
    let finish: (r: ChatResponse) => void = () => undefined;
    const send: AskFn = (_req, onToken, onWithdrawn) =>
      new Promise<ChatResponse>((resolve) => {
        onToken?.("secret 85000");
        onWithdrawn?.("Withdrawn for safety.", "AGT-0220");
        finish = resolve;
      });
    render(<Harness ask={send} createConversation={newConversation()} />);
    await ask();
    const live = await screen.findByRole("article", { name: "Copilot answer in progress" });
    expect(live).toHaveTextContent("AGT-0220");
    expect(live).toHaveTextContent("Withdrawn for safety.");
    expect(live).not.toHaveTextContent("85000");
    finish({ answer: "Withdrawn for safety.", citations: [], toolCalls: [], refusal: { code: "AGT-0220", message: "Withdrawn for safety." }, generated: true });
    expect(await screen.findByRole("alert")).toHaveTextContent("AGT-0220");
  });

  it("keeps answer text out of localStorage and sessionStorage", async () => {
    localStorage.clear();
    sessionStorage.clear();
    render(<Harness ask={async () => answer} createConversation={newConversation()} />);
    await ask();
    await screen.findByRole("article", { name: "Copilot answer" });
    expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain("48,200");
    expect(localStorage).toHaveLength(0);
    expect(sessionStorage).toHaveLength(0);
  });
});
