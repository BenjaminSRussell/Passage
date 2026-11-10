import { render, screen } from "@testing-library/react";
import { StatusPill } from "./StatusPill";
import { standardLabel, standardStatuses, statusTone } from "./status";

describe("status standard", () => {
  it("ignores case and separators", () => {
    expect(statusTone("IN_REVIEW")).toBe("info");
    expect(statusTone("in-review")).toBe("info");
    expect(statusTone("In review")).toBe("info");
    expect(standardLabel("IN_REVIEW")).toBe("In review");
  });

  it("keeps meaning to tone: failures are danger, attention is warning, done is success", () => {
    expect(statusTone("FAILED")).toBe("danger");
    expect(statusTone("REJECTED")).toBe("danger");
    expect(statusTone("ON_HOLD")).toBe("warning");
    expect(statusTone("OVERDUE")).toBe("warning");
    expect(statusTone("POSTED")).toBe("success");
    expect(statusTone("CANCELLED")).toBe("neutral");
  });

  it("shows unknown statuses as neutral sentence case", () => {
    expect(statusTone("SOMETHING_NEW")).toBe("neutral");
    expect(standardLabel("SOMETHING_NEW")).toBe("Something new");
  });

  it("names each status once", () => {
    const names = standardStatuses().map((s) => s.status);
    expect(new Set(names).size).toBe(names.length);
  });

  it("always shows the label as text, so colour is never the only cue", () => {
    render(<StatusPill status="on_hold" />);
    expect(screen.getByText("On hold")).toBeInTheDocument();
  });
});
