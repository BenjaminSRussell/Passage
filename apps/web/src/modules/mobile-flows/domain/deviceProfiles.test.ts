import { describe, expect, it } from "vitest";
import { INTENT_DATA_KEYS, parseIntent } from "./scanBridge";
import {
  checkWebView,
  DEVICE_PROFILES,
  deviceProblems,
  findDeviceProfile,
  SCANNER_PROFILES,
  webViewMajor,
} from "./deviceProfiles";

const GOOD_UA =
  "Mozilla/5.0 (Linux; Android 13; TC52) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.6099.144 Mobile Safari/537.36";
const OLD_UA = GOOD_UA.replace("Chrome/120.0.6099.144", "Chrome/98.0.4758.101");

describe("device profile fixtures", () => {
  it("ships the two PRD fixtures, zebra-tc52 and honeywell-ct45", () => {
    expect(DEVICE_PROFILES.map((p) => p.code)).toEqual(["zebra-tc52", "honeywell-ct45"]);
  });

  it("uses only scanner profiles allowed by §6.32.13", () => {
    for (const p of DEVICE_PROFILES) expect(SCANNER_PROFILES).toContain(p.scannerProfile);
  });

  it("pairs each profile with the intent key its capture bridge reads", () => {
    const zebra = findDeviceProfile("zebra-tc52");
    const honeywell = findDeviceProfile("honeywell-ct45");
    expect(zebra).toMatchObject({ captureSource: "DATAWEDGE", intentDataKey: INTENT_DATA_KEYS.DATAWEDGE });
    expect(honeywell).toMatchObject({ captureSource: "HONEYWELL_DC", intentDataKey: INTENT_DATA_KEYS.HONEYWELL_DC });
  });

  it("the profile's capture source parses the intent its device sends", () => {
    for (const p of DEVICE_PROFILES) {
      const key = p.intentDataKey as string;
      const detail = { source: p.captureSource, extras: { [key]: "]C10109501234567891" } };
      expect(parseIntent(detail)).toEqual({ ok: true, raw: "]C10109501234567891", source: p.captureSource });
    }
  });

  it("looks up profiles case-insensitively and returns undefined for unknown codes", () => {
    expect(findDeviceProfile(" ZEBRA-TC52 ")?.model).toBe("TC52");
    expect(findDeviceProfile("zebra-tc70")).toBeUndefined();
  });
});

describe("webViewMajor", () => {
  it("reads the Chrome major version from an Android WebView user agent", () => {
    expect(webViewMajor(GOOD_UA)).toBe(120);
    expect(webViewMajor(OLD_UA)).toBe(98);
  });

  it("returns null when the user agent has no Chrome token", () => {
    expect(webViewMajor("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)")).toBeNull();
  });
});

describe("checkWebView", () => {
  it("accepts the minimum version and above (§6.32.12: 100)", () => {
    expect(checkWebView(100)).toBeNull();
    expect(checkWebView(120)).toBeNull();
  });

  it("rejects older versions with the PWA fallback advice", () => {
    expect(checkWebView(99)).toBe("WebView 99 is below the minimum 100; use the PWA fallback");
  });

  it("rejects an unknown version", () => {
    expect(checkWebView(null)).toMatch(/unknown/);
  });
});

describe("deviceProblems", () => {
  it("is empty for a known profile on a supported WebView", () => {
    expect(deviceProblems("zebra-tc52", GOOD_UA)).toEqual([]);
    expect(deviceProblems("honeywell-ct45", GOOD_UA)).toEqual([]);
  });

  it("reports an unknown profile and an old WebView together", () => {
    expect(deviceProblems("zebra-tc70", OLD_UA)).toEqual([
      "unknown device profile zebra-tc70",
      "WebView 98 is below the minimum 100; use the PWA fallback",
    ]);
  });
});
