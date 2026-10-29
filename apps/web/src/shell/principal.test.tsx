import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { devDuties, parseDuties, PrincipalProvider, usePrincipalDuties } from "./principal";

function Probe() {
  return <p>{usePrincipalDuties().join("|") || "none"}</p>;
}

describe("principal duties", () => {
  it("splits a comma-separated list, trimming blanks", () => {
    expect(parseDuties(" org.read , org.hierarchy.write,,")).toEqual(["org.read", "org.hierarchy.write"]);
    expect(parseDuties("")).toEqual([]);
    expect(parseDuties(undefined)).toEqual([]);
  });

  it("devDuties is empty when the build defines none", () => {
    expect(devDuties()).toEqual([]);
  });

  it("supplies duties to consumers and defaults to none without a provider", () => {
    const { unmount } = render(<Probe />);
    expect(screen.getByText("none")).toBeInTheDocument();
    unmount();
    render(
      <PrincipalProvider duties={["org.read", "org.hierarchy.write"]}>
        <Probe />
      </PrincipalProvider>,
    );
    expect(screen.getByText("org.read|org.hierarchy.write")).toBeInTheDocument();
  });
});
