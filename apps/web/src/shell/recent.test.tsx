import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { RecentProvider, recentKey, useRecentPages } from "./recent";

const PAGE = { to: "/ledger/transactions", label: "Transactions" };
const UNRELATED = "depot.pins.v1.ann";

let clear: () => void = () => undefined;

function Probe() {
  const recent = useRecentPages();
  clear = recent.clear;
  return <p>{recent.pages.map((p) => p.label).join(",") || "none"}</p>;
}

const mount = (identity: string) =>
  render(
    <RecentProvider identity={identity}>
      <Probe />
    </RecentProvider>,
  );

beforeEach(() => localStorage.clear());
afterEach(() => {
  cleanup();
  localStorage.clear();
});

describe("RecentProvider clear", () => {
  it("empties the pages and removes only this identity's recents key", () => {
    localStorage.setItem(recentKey("ann"), JSON.stringify([PAGE]));
    localStorage.setItem(recentKey("bob"), JSON.stringify([PAGE]));
    localStorage.setItem(UNRELATED, "[]");
    mount("ann");
    expect(screen.getByText("Transactions")).toBeInTheDocument();
    act(() => clear());
    expect(screen.getByText("none")).toBeInTheDocument();
    expect(localStorage.getItem(recentKey("ann"))).toBeNull();
    expect(localStorage.getItem(recentKey("bob"))).not.toBeNull();
    expect(localStorage.getItem(UNRELATED)).toBe("[]");
  });

  it("is harmless when nothing is stored", () => {
    mount("ann");
    expect(() => act(() => clear())).not.toThrow();
    expect(screen.getByText("none")).toBeInTheDocument();
  });

  it("removes nothing for an empty identity", () => {
    localStorage.setItem(recentKey(""), JSON.stringify([PAGE]));
    mount("");
    act(() => clear());
    expect(localStorage.getItem(recentKey(""))).not.toBeNull();
  });
});
