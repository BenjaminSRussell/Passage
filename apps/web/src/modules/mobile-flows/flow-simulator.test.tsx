import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PICK_FLOW, RECEIVE_PUTAWAY_FLOW } from "./domain/coreFlows";
import { FlowSimulator } from "./FlowSimulator";
import mobileFlows from "./module";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

/** A clock the test advances, so step timings are exact. */
function clock(start = 1_800_000_000_000) {
  let t = start;
  return { now: () => t, tick: (ms: number) => (t += ms) };
}

function scan(value: string) {
  fireEvent.change(screen.getByLabelText("Virtual scanner"), { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: "Scan" }));
}

describe("flow simulator", () => {
  it("is part of the designer page and opens on the bundled receive/put-away flow", () => {
    const { container } = render(<MemoryRouter>{mobileFlows.routes[0].element}</MemoryRouter>);
    expect(screen.getByRole("heading", { name: "Flow simulator" })).toBeInTheDocument();
    expect(JSON.parse((screen.getByLabelText("Flow definition (JSON)") as HTMLTextAreaElement).value)).toEqual(RECEIVE_PUTAWAY_FLOW);
    fireEvent.click(screen.getByRole("button", { name: "Run flow" }));
    expect(screen.getByText("Scan the license plate")).toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("loads another bundled core flow into the editor and runs it", () => {
    render(<MemoryRouter>{mobileFlows.routes[0].element}</MemoryRouter>);
    fireEvent.change(screen.getByLabelText("Start from"), { target: { value: "PICK" } });
    expect(JSON.parse((screen.getByLabelText("Flow definition (JSON)") as HTMLTextAreaElement).value)).toEqual(PICK_FLOW);
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(screen.getByRole("status")).toHaveTextContent("Flow graph is valid");
    fireEvent.click(screen.getByRole("button", { name: "Run flow" }));
    expect(screen.getByText("Scan the pick location")).toBeInTheDocument();
  });

  it("runs the flow end to end, rejecting a wrong scan and ignoring a scan on the confirm step", () => {
    const c = clock();
    render(<FlowSimulator flowText={JSON.stringify(RECEIVE_PUTAWAY_FLOW)} now={c.now} />);
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Run flow" }));
    expect(screen.getByText("No steps run yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();

    c.tick(2_000);
    scan("A-01-02");
    expect(screen.getByRole("alert")).toHaveTextContent("MOB-0101 Expected LP, scanned location.");
    expect(screen.getByText("Scan the license plate")).toBeInTheDocument();

    c.tick(1_000);
    scan("]C100340123456789012340");
    expect(screen.getByText("Scan the put-away location")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    c.tick(1_500);
    scan("A-01-02");
    expect(screen.getByText("Confirm put-away")).toBeInTheDocument();

    scan("]C100340123456789012340");
    expect(screen.getByRole("alert")).toHaveTextContent("Scan ignored: the focused step is a confirm step, not a scan step.");
    expect(within(screen.getByRole("list", { name: "Ignored scans" })).getAllByRole("listitem")).toHaveLength(1);

    c.tick(500);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    const captured = screen.getByRole("list", { name: "Captured values" });
    expect(captured).toHaveTextContent("scan_lp: ]C100340123456789012340");
    expect(captured).toHaveTextContent("scan_location: A-01-02");
    expect(captured).toHaveTextContent("confirm_putaway: confirmed");

    // The LP step took two attempts (one error); timings are measured from when the step opened.
    const rows = within(screen.getByRole("table", { name: "Step timing" })).getAllByRole("row");
    const lp = rows.find((r) => within(r).queryByText("scan_lp"));
    expect(lp).toBeDefined();
    expect(within(lp as HTMLElement).getAllByRole("cell").map((cell) => cell.textContent)).toEqual(["scan_lp", "2", "1", "2", "3"]);
  });

  it("steps back to the previous step", () => {
    render(<FlowSimulator flowText={JSON.stringify(RECEIVE_PUTAWAY_FLOW)} />);
    fireEvent.click(screen.getByRole("button", { name: "Run flow" }));
    scan("LP-000123");
    expect(screen.getByText("Scan the put-away location")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByText("Scan the license plate")).toBeInTheDocument();
  });

  it("takes values on input steps and outcomes on branch steps", () => {
    const flow = {
      code: "COUNT_SPOT",
      entryStepId: "lot_tracked",
      steps: [
        { id: "lot_tracked", type: "branch", prompt: "Is the item lot tracked?", next: { yes: "qty", no: null } },
        { id: "qty", type: "input", prompt: "Enter the counted quantity", next: { default: null } },
      ],
    };
    render(<FlowSimulator flowText={JSON.stringify(flow)} />);
    fireEvent.click(screen.getByRole("button", { name: "Run flow" }));
    fireEvent.click(screen.getByRole("button", { name: "Choose" }));
    expect(screen.getByRole("alert")).toHaveTextContent('MOB-0113 Outcome "" is not defined on step lot_tracked.');
    fireEvent.change(screen.getByLabelText("Outcome"), { target: { value: "yes" } });
    fireEvent.click(screen.getByRole("button", { name: "Choose" }));
    expect(screen.getByText("Enter the counted quantity")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Enter" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Step qty needs a value.");
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "Enter" }));
    expect(screen.getByRole("list", { name: "Captured values" })).toHaveTextContent("qty: 12");
  });

  it("refuses to run a flow that fails validation or is not JSON", () => {
    const { rerender } = render(<FlowSimulator flowText="{not json" />);
    fireEvent.click(screen.getByRole("button", { name: "Run flow" }));
    expect(screen.getByRole("list", { name: "Simulator issues" })).toHaveTextContent("MOB-0113: The flow is not valid JSON.");
    const broken = { ...RECEIVE_PUTAWAY_FLOW, entryStepId: "nowhere" };
    rerender(<FlowSimulator flowText={JSON.stringify(broken)} />);
    fireEvent.click(screen.getByRole("button", { name: "Run flow" }));
    expect(screen.getByRole("list", { name: "Simulator issues" })).toHaveTextContent("entry step nowhere does not exist");
    expect(screen.queryByLabelText("Virtual scanner")).not.toBeInTheDocument();
  });
});
