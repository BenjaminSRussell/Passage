import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { pageViolations } from "./a11yTest";
import { inputFields, listTools, MCP_PATH, toolDomain, type McpTool } from "./api/mcp";
import copilot from "./module";
import { ToolCatalogPage } from "./ToolCatalogPage";
import { ToolDetailPage } from "./ToolDetailPage";

const tool = (name: string, duty: string, approval: McpTool["x-passage-approval"], readOnly = true): McpTool => ({
  name,
  description: `${name} description`,
  inputSchema: {
    type: "object",
    properties: { id: { type: "string", description: "Invoice id" }, limit: { type: "integer" } },
    required: ["id"],
  },
  annotations: { readOnlyHint: readOnly, destructiveHint: !readOnly, idempotentHint: readOnly },
  "x-passage-duty": duty,
  "x-passage-approval": approval,
});

const catalog = [
  tool("passage.ap.invoice.list", "APInvoiceRead", "none"),
  tool("passage.ap.invoice.propose_coding", "APInvoiceCode", "suggest", false),
  tool("passage.ledger.journal.reverse", "LedgerPost", "approve_before_commit", false),
];

const rpc = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("listTools", () => {
  it("posts JSON-RPC tools/list to the gateway path and returns the validated tools", async () => {
    const fetchMock = vi.fn(async () => rpc({ jsonrpc: "2.0", id: "x", result: { tools: catalog } }));
    const tools = await listTools(undefined, fetchMock as unknown as typeof fetch);
    expect(tools.map((t) => t.name)).toEqual(catalog.map((t) => t.name));
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(MCP_PATH);
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toMatchObject({ jsonrpc: "2.0", method: "tools/list" });
  });

  it("raises the HTTP status, a JSON-RPC error and an unreadable body", async () => {
    await expect(listTools(undefined, (async () => new Response("x", { status: 502 })) as unknown as typeof fetch)).rejects.toThrow("HTTP 502");
    await expect(
      listTools(undefined, (async () => rpc({ jsonrpc: "2.0", id: 1, error: { code: -32600, message: "invalid JSON-RPC request" } })) as unknown as typeof fetch),
    ).rejects.toThrow("invalid JSON-RPC request");
    await expect(listTools(undefined, (async () => rpc({ jsonrpc: "2.0", id: 1, result: { tools: [{ name: 1 }] } })) as unknown as typeof fetch)).rejects.toThrow(
      "couldn't read",
    );
  });

  it("derives the domain and flattens input fields", () => {
    expect(toolDomain("passage.ap.invoice.list")).toBe("ap");
    expect(inputFields(catalog[0]!.inputSchema)).toEqual([
      { name: "id", type: "string", required: true, description: "Invoice id" },
      { name: "limit", type: "integer", required: false, description: "" },
    ]);
    expect(inputFields({})).toEqual([]);
  });
});

describe("ToolCatalogPage", () => {
  it("shows a loading table, then one row per tool with duty and approval", async () => {
    let resolve: (t: McpTool[]) => void = () => {};
    const load = () => new Promise<McpTool[]>((r) => (resolve = r));
    render(<ToolCatalogPage load={load} />);
    expect(screen.getByRole("table", { name: "Tools" })).toHaveAttribute("aria-busy", "true");
    resolve(catalog);
    const table = await screen.findByRole("table", { name: "Tools" });
    await waitFor(() => expect(table).not.toHaveAttribute("aria-busy", "true"));
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    expect(table).toHaveTextContent("passage.ledger.journal.reverse");
    expect(table).toHaveTextContent("LedgerPost");
    expect(table).toHaveTextContent("Approve before commit");
    expect(pageViolations()).toEqual([]);
  });

  it("filters by name or duty and by approval mode, with a no-match message", async () => {
    render(<ToolCatalogPage load={async () => catalog} />);
    const user = userEvent.setup();
    const table = await screen.findByRole("table", { name: "Tools" });
    await waitFor(() => expect(table).toHaveTextContent("passage.ap.invoice.list"));
    await user.selectOptions(screen.getByLabelText("Approval mode"), "suggest");
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    expect(table).toHaveTextContent("passage.ap.invoice.propose_coding");
    await user.type(screen.getByLabelText("Tool or duty"), "ledger");
    expect(await screen.findByText("No tools match")).toBeInTheDocument();
  });

  it("explains an empty catalog as missing duties", async () => {
    render(<ToolCatalogPage load={async () => []} />);
    expect(await screen.findByText("No tools available to you")).toBeInTheDocument();
  });

  it("shows the error with a working Try again", async () => {
    const load = vi.fn().mockRejectedValueOnce(new Error("The tool catalog is unavailable (HTTP 503).")).mockResolvedValueOnce(catalog);
    render(<ToolCatalogPage load={load} />);
    const user = userEvent.setup();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("HTTP 503"));
    expect(screen.queryByRole("table", { name: "Tools" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("passage.ap.invoice.list")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("opens a tool from its row", async () => {
    const onOpen = vi.fn();
    render(<ToolCatalogPage load={async () => catalog} onOpen={onOpen} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "passage.ap.invoice.propose_coding" }));
    expect(onOpen).toHaveBeenCalledWith("passage.ap.invoice.propose_coding");
  });
});

describe("ToolDetailPage", () => {
  it("shows governance facts and the arguments of one tool", async () => {
    render(<ToolDetailPage name="passage.ap.invoice.propose_coding" load={async () => catalog} />);
    expect(await screen.findByRole("heading", { name: "passage.ap.invoice.propose_coding" })).toBeInTheDocument();
    expect(screen.getByText("APInvoiceCode")).toBeInTheDocument();
    expect(screen.getByText("Changes data")).toBeInTheDocument();
    const args = screen.getByRole("table", { name: "Arguments" });
    expect(args).toHaveTextContent("id");
    expect(args).toHaveTextContent("Required");
    expect(args).toHaveTextContent("Invoice id");
    expect(pageViolations()).toEqual([]);
  });

  it("shows a loading state first", () => {
    render(<ToolDetailPage name="passage.ap.invoice.list" load={() => new Promise(() => {})} />);
    expect(screen.getByText("Loading tool…")).toBeInTheDocument();
  });

  it("says a tool outside your duties is not available", async () => {
    render(<ToolDetailPage name="passage.payroll.run.post" load={async () => catalog} />);
    expect(await screen.findByText("This tool isn't available to you")).toBeInTheDocument();
  });

  it("shows a load failure as an alert", async () => {
    render(<ToolDetailPage name="passage.ap.invoice.list" load={async () => { throw new Error("The tool catalog is unavailable (HTTP 500)."); }} />);
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("HTTP 500"));
  });
});

describe("copilot routes", () => {
  it("navigates from the catalog to the tool's detail page through module.ts", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => rpc({ jsonrpc: "2.0", id: "x", result: { tools: catalog } })));
    render(
      <MemoryRouter initialEntries={["/copilot/tools"]}>
        <Routes>
          {copilot.routes.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
        </Routes>
      </MemoryRouter>,
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "passage.ledger.journal.reverse" }));
    expect(await screen.findByRole("heading", { name: "passage.ledger.journal.reverse" })).toBeInTheDocument();
    expect(screen.getByText("LedgerPost")).toBeInTheDocument();
  });
});
