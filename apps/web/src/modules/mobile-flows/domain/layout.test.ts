import { describe, expect, it } from "vitest";
import { MIN_FONT_PX, LARGE_TARGET_PX, TARGET_PX, fitsViewport, fontSizePx, targetSizePx, textDirection } from "./layout";

describe("textDirection", () => {
  it.each([
    ["ar-SA", "rtl"],
    ["he", "rtl"],
    ["fa_IR", "rtl"],
    ["en-US", "ltr"],
    ["de", "ltr"],
    ["", "ltr"],
  ])("%s is %s", (locale, want) => {
    expect(textDirection(locale)).toBe(want);
  });
});

describe("fontSizePx", () => {
  it("never goes below the 16 px floor", () => {
    expect(fontSizePx(12, false)).toBe(MIN_FONT_PX);
    expect(fontSizePx(16, false)).toBe(16);
  });

  it("scales up in large-target mode", () => {
    expect(fontSizePx(16, true)).toBe(20);
    expect(fontSizePx(18, true)).toBe(23);
  });
});

describe("targets and viewport", () => {
  it("uses the large target only in large-target mode", () => {
    expect(targetSizePx(false)).toBe(TARGET_PX);
    expect(targetSizePx(true)).toBe(LARGE_TARGET_PX);
  });

  it.each([
    [319, false],
    [320, true],
    [1280, true],
    [Number.NaN, false],
  ])("viewport %d fits: %s", (width, want) => {
    expect(fitsViewport(width)).toBe(want);
  });
});
