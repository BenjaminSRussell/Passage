import { describe, expect, it } from "vitest";
import { buildCatalog } from "../src/catalog.js";
import { handleRpc, RPC, type RpcResponse } from "../src/jsonrpc.js";
import { principal, readTool, rig } from "./fixtures.js";

/**
 * FR-47.2 contract, gateway side, over a generated 400-tool catalog. Tool i
 * requires duty Duty(i mod 20); the caller holds Duty0..Duty9, so exactly half
 * of the tools are visible and callable. The live-API half of the contract
 * (the API's own 403) needs the services and is a later slice.
 */
const TOOL_COUNT = 400;
const DUTY_COUNT = 20;
const HELD = new Set(Array.from({ length: DUTY_COUNT / 2 }, (_, i) => `Duty${i}`));

const tools = Array.from({ length: TOOL_COUNT }, (_, i) => readTool(`passage.dom${i}.entity.read`, `Duty${i % DUTY_COUNT}`));

function dutyOf(i: number): string {
  return `Duty${i % DUTY_COUNT}`;
}

describe("gateway permission contract over 400 tools (FR-47.2, AC-47.1)", () => {
  it("tools/list returns exactly the tools whose duty the caller holds", async () => {
    const r = rig(buildCatalog(tools));
    const reply = (await handleRpc(r.deps, principal([...HELD]), { jsonrpc: "2.0", id: 1, method: "tools/list" })) as unknown as {
      result: { tools: Array<{ name: string; "x-passage-duty": string }> };
    };
    const listed = reply.result.tools;
    expect(listed).toHaveLength(TOOL_COUNT / 2);
    expect(listed.every((t) => HELD.has(t["x-passage-duty"]))).toBe(true);
  });

  it("every tool without the duty is refused with -32003 and never reaches upstream", async () => {
    const r = rig(buildCatalog(tools));
    const p = principal([...HELD]);
    let refused = 0;
    for (let i = 0; i < TOOL_COUNT; i++) {
      if (HELD.has(dutyOf(i))) continue;
      const reply: RpcResponse | undefined = await handleRpc(r.deps, p, {
        jsonrpc: "2.0",
        id: i,
        method: "tools/call",
        params: { name: `passage.dom${i}.entity.read`, arguments: {} },
      });
      expect(reply && "error" in reply ? reply.error.code : undefined).toBe(RPC.forbidden);
      refused += 1;
    }
    expect(refused).toBe(TOOL_COUNT / 2);
    expect(r.upstream.calls).toHaveLength(0);
  });

  it("every tool with the duty is passed through to upstream once", async () => {
    const r = rig(buildCatalog(tools));
    const p = principal([...HELD]);
    for (let i = 0; i < TOOL_COUNT; i++) {
      if (!HELD.has(dutyOf(i))) continue;
      const reply = await handleRpc(r.deps, p, {
        jsonrpc: "2.0",
        id: i,
        method: "tools/call",
        params: { name: `passage.dom${i}.entity.read`, arguments: {} },
      });
      expect(reply && "error" in reply ? reply.error.code : undefined).not.toBe(RPC.forbidden);
    }
    expect(r.upstream.calls).toHaveLength(TOOL_COUNT / 2);
    expect(r.upstream.calls.every((c) => HELD.has(c.tool.requiredDuty))).toBe(true);
  });
});
