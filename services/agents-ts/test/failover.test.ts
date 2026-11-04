import { describe, expect, it } from "vitest";
import { AgentError } from "../src/errors.js";
import type { ModelProvider, ModelResponse } from "../src/runtime/engine.js";
import { createFailoverProvider, type ProviderEndpoint } from "../src/providers/failover.js";
import { providerRejected, providerUnavailable } from "../src/providers/wire.js";
import { proposal } from "./fakes.js";

const REQ = { system: "s", user: "u", allowedTools: ["passage.ap.invoice.post"] } as const;

/** Provider that has an outage (AGT-0206, 503) while `down` is true, else returns a proposal. */
class Flaky implements ModelProvider {
  calls = 0;
  down = false;
  /** Whether the outage proves nothing was billed (a 503) or not (a timeout). */
  unbilled = true;
  constructor(private readonly name: string) {}
  async complete(): Promise<ModelResponse> {
    this.calls += 1;
    if (this.down) throw providerUnavailable(this.unbilled ? `${this.name} returned 503` : `${this.name} timed out`, this.unbilled);
    return proposal(0.9);
  }
}

function setup(opts: { residencyRegion: string | null; secondaryRegion?: string; withSecondary?: boolean }) {
  const primaryP = new Flaky("primary");
  const secondaryP = new Flaky("secondary");
  let now = 0;
  const primary: ProviderEndpoint = { name: "anthropic-eu", region: "EU", provider: primaryP };
  const secondary: ProviderEndpoint | undefined =
    opts.withSecondary === false ? undefined : { name: "azure-eu", region: opts.secondaryRegion ?? "EU", provider: secondaryP };
  const provider = createFailoverProvider({
    primary,
    secondary,
    residencyRegion: opts.residencyRegion,
    residencyAllows: (endpointRegion, residency) => endpointRegion === residency,
    nowMs: () => now,
  });
  return { provider, primaryP, secondaryP, advance: (ms: number) => (now += ms) };
}

describe("createFailoverProvider (BR-47.9)", () => {
  it("serves from the primary while it is healthy", async () => {
    const { provider, primaryP, secondaryP } = setup({ residencyRegion: "EU" });
    await provider.complete(REQ);
    expect(provider.lastServedBy()).toBe("anthropic-eu");
    expect([primaryP.calls, secondaryP.calls]).toEqual([1, 0]);
  });

  it("surfaces a primary outage inside the 60 s window without failing over", async () => {
    const { provider, primaryP, secondaryP, advance } = setup({ residencyRegion: "EU" });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(59_000);
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    expect(secondaryP.calls).toBe(0);
  });

  it("fails over to a residency-compliant secondary after 60 s of outage", async () => {
    const { provider, primaryP, secondaryP, advance } = setup({ residencyRegion: "EU" });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(60_000);
    await provider.complete(REQ);
    expect(provider.lastServedBy()).toBe("azure-eu");
    expect(secondaryP.calls).toBe(1);
  });

  it("pauses with AGT-0206 and never calls a non-compliant secondary", async () => {
    const { provider, primaryP, secondaryP, advance } = setup({ residencyRegion: "EU", secondaryRegion: "US" });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(60_000);
    await expect(provider.complete(REQ)).rejects.toThrow(/paused/);
    expect(secondaryP.calls).toBe(0);
  });

  it("a pause is unbilled when no provider was asked, and follows the primary's failure when it was tried", async () => {
    const { provider, primaryP, advance } = setup({ residencyRegion: null, withSecondary: false });
    primaryP.down = true;
    primaryP.unbilled = false;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ unbilled: false });
    advance(60_000);
    await expect(provider.complete(REQ)).rejects.toMatchObject({ message: expect.stringMatching(/paused/), unbilled: false });
    const before = primaryP.calls;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ message: expect.stringMatching(/paused/), unbilled: true });
    expect(primaryP.calls).toBe(before);
    primaryP.unbilled = true;
    advance(30_000);
    await expect(provider.complete(REQ)).rejects.toMatchObject({ message: expect.stringMatching(/paused/), unbilled: true });
  });

  it("pauses when no secondary is configured", async () => {
    const { provider, primaryP, advance } = setup({ residencyRegion: null, withSecondary: false });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(60_000);
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
  });

  it("clears the outage clock when the primary recovers", async () => {
    const { provider, primaryP, secondaryP, advance } = setup({ residencyRegion: "EU" });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    primaryP.down = false;
    advance(10_000);
    await provider.complete(REQ);
    expect(provider.lastServedBy()).toBe("anthropic-eu");
    // A fresh failure must again wait the full window before failing over.
    primaryP.down = true;
    advance(1_000);
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(59_000);
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    expect(secondaryP.calls).toBe(0);
  });

  it("rethrows non-availability errors unchanged and does not start the clock", async () => {
    const bad: ModelProvider = {
      async complete() {
        throw new AgentError("AGT-0214", "tool failed");
      },
    };
    const provider = createFailoverProvider({
      primary: { name: "p", region: "EU", provider: bad },
      residencyRegion: "EU",
      residencyAllows: () => true,
      nowMs: () => 0,
    });
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0214" });
  });

  it("a provider that answers but rejects the request never starts the outage clock", async () => {
    let calls = 0;
    const rejecting: ModelProvider = {
      async complete() {
        calls += 1;
        throw providerRejected("provider rejected the request with 400");
      },
    };
    const secondaryP = new Flaky("secondary");
    let now = 0;
    const provider = createFailoverProvider({
      primary: { name: "p", region: "EU", provider: rejecting },
      secondary: { name: "s", region: "EU", provider: secondaryP },
      residencyRegion: "EU",
      residencyAllows: () => true,
      nowMs: () => now,
    });
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206", outage: false });
    now += 120_000;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206", outage: false });
    expect(calls).toBe(2);
    expect(secondaryP.calls).toBe(0);
  });

  it("serves the secondary within the same call once the window has passed", async () => {
    const { provider, primaryP, secondaryP, advance } = setup({ residencyRegion: "EU" });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(60_000);
    // The primary is due for a retry, fails again, and the call falls through to the secondary.
    await expect(provider.complete(REQ)).resolves.toBeDefined();
    expect([primaryP.calls, secondaryP.calls]).toEqual([2, 1]);
  });

  it("while failed over, retries the primary once per 30 s and returns to it when it recovers", async () => {
    const { provider, primaryP, secondaryP, advance } = setup({ residencyRegion: "EU" });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(60_000);
    await provider.complete(REQ); // primary retried and failed, served by secondary
    advance(10_000);
    await provider.complete(REQ); // primary not due: straight to the secondary
    expect([primaryP.calls, secondaryP.calls]).toEqual([2, 2]);
    primaryP.down = false;
    advance(20_000);
    await provider.complete(REQ); // primary due again and healthy
    expect(provider.lastServedBy()).toBe("anthropic-eu");
    advance(1_000);
    await provider.complete(REQ);
    expect(provider.lastServedBy()).toBe("anthropic-eu");
    expect(secondaryP.calls).toBe(2);
  });

  it("a paused tenant without a secondary resumes once the primary recovers", async () => {
    const { provider, primaryP, advance } = setup({ residencyRegion: null, withSecondary: false });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(60_000);
    await expect(provider.complete(REQ)).rejects.toThrow(/paused/);
    primaryP.down = false;
    advance(30_000);
    await expect(provider.complete(REQ)).resolves.toBeDefined();
    expect(provider.lastServedBy()).toBe("anthropic-eu");
  });

  it("the health probe's report sends the next call straight back to the primary", async () => {
    const { provider, primaryP, secondaryP, advance } = setup({ residencyRegion: "EU" });
    primaryP.down = true;
    await expect(provider.complete(REQ)).rejects.toMatchObject({ code: "AGT-0206" });
    advance(60_000);
    await provider.complete(REQ);
    primaryP.down = false;
    advance(1_000);
    provider.reportPrimaryHealthy();
    await provider.complete(REQ);
    expect(provider.lastServedBy()).toBe("anthropic-eu");
    expect(secondaryP.calls).toBe(1);
  });
});
