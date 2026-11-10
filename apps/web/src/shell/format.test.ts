import { describe, expect, it } from "vitest";
import { formatDate, isPastDate } from "./format";

describe("dates", () => {
  it("shows a date-only value on its own day whatever the time zone", () => {
    expect(formatDate("2026-01-31")).toBe("Jan 31, 2026");
  });

  it("is overdue only after the due day has ended in the user's zone", () => {
    expect(isPastDate("2026-11-10", new Date(2026, 10, 10, 23, 59))).toBe(false);
    expect(isPastDate("2026-11-10", new Date(2026, 10, 11, 0, 1))).toBe(true);
  });

  it("ignores values that are not dates", () => {
    expect(formatDate("soon")).toBe("");
    expect(isPastDate("soon")).toBe(false);
  });
});
