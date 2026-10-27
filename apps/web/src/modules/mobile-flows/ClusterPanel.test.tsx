import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ClusterPanel, incompleteLine, parseWorks } from "./ClusterPanel";

function lines(n: number): string {
  return Array.from({ length: n }, (_, i) => `W${i + 1},O${i + 1},LP${i + 1}`).join("\n");
}

function build(text: string, maxPositions?: number) {
  render(<ClusterPanel maxPositions={maxPositions} />);
  fireEvent.change(screen.getByLabelText("Works (workId, orderId, LP per line)"), { target: { value: text } });
  fireEvent.click(screen.getByRole("button", { name: "Build cluster" }));
}

function scanPosition(value: string) {
  fireEvent.change(screen.getByRole("textbox", { name: /Scan cart position/ }), { target: { value } });
  fireEvent.submit(screen.getByRole("form", { name: "Scan cart position" }));
}

function positions() {
  return within(screen.getByRole("list", { name: "Cart positions" }));
}

describe("parseWorks", () => {
  it.each([
    ["", []],
    ["  \n\n", []],
    ["W1,O1,LP1", [{ workId: "W1", orderId: "O1", lp: "LP1" }]],
    [
      " W1 , O1 , LP1 \nW2,O2,LP2",
      [
        { workId: "W1", orderId: "O1", lp: "LP1" },
        { workId: "W2", orderId: "O2", lp: "LP2" },
      ],
    ],
    ["W1,O1", [{ workId: "W1", orderId: "O1", lp: "" }]],
  ])("parses %j", (text, want) => {
    expect(parseWorks(text)).toEqual(want);
  });

  it("finds the first incomplete line", () => {
    expect(incompleteLine(parseWorks("W1,O1,LP1\nW2,O2"))).toBe(2);
    expect(incompleteLine(parseWorks(lines(3)))).toBeNull();
  });
});

describe("ClusterPanel (BR-32.5)", () => {
  it("refuses an empty cluster", () => {
    build("");
    expect(screen.getByRole("alert")).toHaveTextContent("at least one work");
  });

  it("refuses a work without its LP, so no position is left unbound", () => {
    build("W1,O1,LP1\nW2,O2");
    expect(screen.getByRole("alert")).toHaveTextContent("Line 2 needs a work, an order and an LP.");
    expect(screen.queryByRole("list", { name: "Cart positions" })).toBeNull();
  });

  it("caps the cluster at 8 positions even when the menu allows more", () => {
    build(lines(9), 24);
    expect(screen.getByRole("alert")).toHaveTextContent("at most 8 works; got 9");
  });

  it("refuses two works for the same order's LP", () => {
    build("W1,O1,LP1\nW2,O1,LP2");
    expect(screen.getByRole("alert")).toHaveTextContent("Each order and LP can appear once");
  });

  it("binds each position to one order's LP and rejects a wrong cart position (MOB-0109)", () => {
    build(lines(2));
    expect(positions().getByText(/Position 1: LP LP1 for order O1 · OPEN/)).toBeInTheDocument();

    scanPosition("2");
    expect(screen.getByRole("alert")).toHaveTextContent("MOB-0109 Wrong cart position: scanned 2, expected 1.");
    expect(screen.getByRole("button", { name: "Complete position 1" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Short position 1" })).toBeDisabled();
  });

  it("completes a position only after the right position is scanned, then finishes the cluster", () => {
    build(lines(2));
    expect(screen.getByRole("button", { name: "Complete position 1" })).toBeDisabled();
    scanPosition("1");
    expect(screen.getByRole("status")).toHaveTextContent("Position 1 confirmed for LP LP1.");
    fireEvent.click(screen.getByRole("button", { name: "Complete position 1" }));
    expect(screen.getByRole("status")).toHaveTextContent("Position 1 picked in full.");

    // The next position needs its own scan: the confirmation of position 1 does not carry over.
    expect(screen.getByRole("button", { name: "Complete position 2" })).toBeDisabled();
    scanPosition("2");
    fireEvent.click(screen.getByRole("button", { name: "Complete position 2" }));
    expect(screen.getByText("Cluster finished: every position is picked or short.")).toBeInTheDocument();
    expect(positions().getByText(/Position 2: .* · DONE/)).toBeInTheDocument();
  });

  it("a short pick on one position does not release the others", () => {
    build(lines(3));
    scanPosition("1");
    fireEvent.click(screen.getByRole("button", { name: "Short position 1" }));
    expect(positions().getByText(/Position 1: .* · SHORT/)).toBeInTheDocument();
    expect(positions().getByText(/Position 2: LP LP2 for order O2 · OPEN/)).toBeInTheDocument();
    expect(positions().getByText(/Position 3: LP LP3 for order O3 · OPEN/)).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Scan cart position 2" })).toBeInTheDocument();
  });

  it.each(["abc", "", "1.0", "-1"])("asks for a position number when the scan is %j", (value) => {
    build(lines(1));
    scanPosition(value);
    expect(screen.getByRole("alert")).toHaveTextContent("Scan a cart position number.");
    expect(screen.getByRole("button", { name: "Complete position 1" })).toBeDisabled();
  });

  it("starts a new cluster from the cart screen", () => {
    build(lines(1));
    fireEvent.click(screen.getByRole("button", { name: "New cluster" }));
    expect(screen.queryByRole("list", { name: "Cart positions" })).toBeNull();
    expect(screen.getByLabelText("Works (workId, orderId, LP per line)")).toHaveValue("");
  });
});
