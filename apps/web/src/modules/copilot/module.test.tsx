import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import copilot from "./module";
import { postChat } from "./api/client";

describe("copilot module", () => {
  it("uses its folder name as id and declares its route and duty-gated nav", () => {
    expect(copilot.id).toBe("copilot");
    expect(copilot.routes.map((r) => r.path)).toEqual(["/copilot", "/copilot/tools", "/copilot/tools/:name"]);
    expect(copilot.nav).toEqual([
      { label: "Copilot", to: "/copilot", duty: "CopilotUse" },
      { label: "Tool catalog", to: "/copilot/tools", duty: "CopilotUse" },
    ]);
    expect(copilot.nav.every((n) => n.to.startsWith("/copilot"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("copilot");
  });

  it("renders the copilot page with a mode group and the send button", () => {
    render(createElement(MemoryRouter, null, copilot.routes[0]?.element));
    expect(screen.getByRole("heading", { name: "Copilot" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Copilot mode" })).toHaveTextContent("Explain this record");
    expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "No answer yet" })).toBeInTheDocument();
  });
});

describe("postChat", () => {
  const body = {
    answer: "ok",
    citations: [],
    toolCalls: [],
    refusal: null,
    generated: true,
  };

  it("posts the request and validates the response shape", async () => {
    let sent: { url: string; init?: RequestInit } | undefined;
    const fakeFetch = (async (url: string, init?: RequestInit) => {
      sent = { url, ...(init ? { init } : {}) };
      return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
    }) as typeof fetch;
    const res = await postChat({ mode: "ask", message: "hi" }, fakeFetch);
    expect(res.answer).toBe("ok");
    expect(sent?.url).toBe("/api/v1/copilot/chat");
    expect(sent?.init?.method).toBe("POST");
  });

  it("rejects a response that is not marked generated", async () => {
    const fakeFetch = (async () =>
      new Response(JSON.stringify({ ...body, generated: false }), { status: 200 })) as typeof fetch;
    await expect(postChat({ mode: "ask", message: "hi" }, fakeFetch)).rejects.toThrow();
  });

  it("surfaces a non-2xx status", async () => {
    const fakeFetch = (async () => new Response("nope", { status: 503 })) as typeof fetch;
    await expect(postChat({ mode: "ask", message: "hi" }, fakeFetch)).rejects.toThrow("HTTP 503");
  });
});
