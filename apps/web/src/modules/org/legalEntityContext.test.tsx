import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LegalEntity } from "./api/client";
import { LegalEntitySwitcher } from "./LegalEntitySwitcher";
import { LegalEntityProvider, useLegalEntity } from "./legalEntityContext";
import { stubLocalStorage } from "./testStorage";

const entity = (id: string, status: "active" | "inactive"): LegalEntity => ({
  id,
  code: id.toUpperCase(),
  name: `Entity ${id}`,
  country: "US",
  currency: "USD",
  timeZone: "UTC",
  defaultLanguage: "en",
  status,
  version: 1,
  createdAt: "",
  updatedAt: "",
});

const entities = [entity("a", "active"), entity("b", "inactive"), entity("c", "active")];
const KEY = "passage.org.legalEntity";
const mounts = vi.fn();
let storage: Storage;

function Probe() {
  const { scope } = useLegalEntity();
  useEffect(() => mounts(), []);
  return <output aria-label="scope">{scope}</output>;
}

function renderProvider(duties?: string[]) {
  return render(
    <LegalEntityProvider duties={duties}>
      <LegalEntitySwitcher />
      <Probe />
    </LegalEntityProvider>,
  );
}

const optionValues = () => screen.getAllByRole("option").map((o) => (o as HTMLOptionElement).value);
const scopeText = () => screen.getByLabelText("scope").textContent;

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ items: entities }))));
  storage = stubLocalStorage();
  mounts.mockClear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("LegalEntityProvider", () => {
  it("excludes inactive entities and defaults to the first active one", async () => {
    renderProvider();
    await waitFor(() => expect(scopeText()).toBe("a"));
    expect(optionValues()).toEqual(["a", "c"]);
  });

  it("restores a persisted choice only while it names an active entity", async () => {
    storage.setItem(KEY, "c");
    const first = renderProvider();
    await waitFor(() => expect(scopeText()).toBe("c"));
    first.unmount();

    storage.setItem(KEY, "b");
    renderProvider();
    await waitFor(() => expect(scopeText()).toBe("a"));
    expect(storage.getItem(KEY)).toBeNull();
  });

  it("offers ALL only with the duty and ignores it otherwise", async () => {
    const without = renderProvider();
    await waitFor(() => expect(scopeText()).toBe("a"));
    expect(optionValues()).not.toContain("ALL");
    without.unmount();

    storage.setItem(KEY, "ALL");
    renderProvider();
    await waitFor(() => expect(scopeText()).toBe("a"));
    expect(storage.getItem(KEY)).toBeNull();
  });

  it("restores a persisted ALL when the duty is held", async () => {
    storage.setItem(KEY, "ALL");
    renderProvider(["org.switcher.all_scope"]);
    await waitFor(() => expect(scopeText()).toBe("ALL"));
    expect(optionValues()).toContain("ALL");
  });

  it("tolerates a throwing localStorage and still switches", async () => {
    vi.spyOn(storage, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(storage, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    renderProvider();
    await waitFor(() => expect(scopeText()).toBe("a"));
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: "c" } });
    expect(scopeText()).toBe("c");
  });

  it("switches synchronously, persists, and does not remount the tree", async () => {
    renderProvider();
    await waitFor(() => expect(scopeText()).toBe("a"));
    act(() => {
      fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: "c" } });
    });
    expect(scopeText()).toBe("c");
    expect(storage.getItem(KEY)).toBe("c");
    expect(mounts).toHaveBeenCalledTimes(1);
  });
});
