// Device profiles for the two demo scanner models (§6.32.9 fixtures `zebra-tc52` and
// `honeywell-ct45`; §6.32.13 scanner_profile). A profile says which capture bridge the
// device uses, which scan source the runner should expect, and the minimum WebView (§6.32.12).
// Pure: lookups and checks only. The shell provisions the actual DataWedge or Honeywell profile.

import { INTENT_DATA_KEYS } from "./scanBridge";
import type { ScanSource } from "./scanRouting";

/** Scanner profiles allowed by §6.32.13 scanner_profile. */
export const SCANNER_PROFILES = ["ZEBRA_DATAWEDGE", "HONEYWELL_DC", "KEYBOARD_WEDGE"] as const;
export type ScannerProfile = (typeof SCANNER_PROFILES)[number];

/** Minimum Android WebView major version for the shell (§6.32.12 mitigation). */
export const MIN_WEBVIEW_MAJOR = 100;

export interface DeviceProfile {
  /** Stable fixture code, e.g. `zebra-tc52`. */
  code: string;
  vendor: "Zebra" | "Honeywell";
  model: string;
  scannerProfile: ScannerProfile;
  /** Scan source the bridge reports for this device. */
  captureSource: ScanSource;
  /** Intent extra that carries the decoded data, when the device uses an intent bridge. */
  intentDataKey?: string;
}

export const DEVICE_PROFILES: readonly DeviceProfile[] = [
  {
    code: "zebra-tc52",
    vendor: "Zebra",
    model: "TC52",
    scannerProfile: "ZEBRA_DATAWEDGE",
    captureSource: "DATAWEDGE",
    intentDataKey: INTENT_DATA_KEYS.DATAWEDGE,
  },
  {
    code: "honeywell-ct45",
    vendor: "Honeywell",
    model: "CT45",
    scannerProfile: "HONEYWELL_DC",
    captureSource: "HONEYWELL_DC",
    intentDataKey: INTENT_DATA_KEYS.HONEYWELL_DC,
  },
];

/** Looks up a profile by fixture code (case-insensitive). */
export function findDeviceProfile(code: string): DeviceProfile | undefined {
  const wanted = code.trim().toLowerCase();
  return DEVICE_PROFILES.find((p) => p.code === wanted);
}

/**
 * Reads the Chrome major version from an Android WebView user agent, for example
 * `Chrome/120.0.6099.144`. Returns null when the string has no Chrome token.
 */
export function webViewMajor(userAgent: string): number | null {
  const m = /Chrome\/(\d+)\./.exec(userAgent);
  return m ? Number(m[1]) : null;
}

/** Returns null when the WebView is supported, otherwise the reason it is not. */
export function checkWebView(major: number | null): string | null {
  if (major === null) return "WebView version unknown; the shell must report its Chrome version";
  if (major < MIN_WEBVIEW_MAJOR) return `WebView ${major} is below the minimum ${MIN_WEBVIEW_MAJOR}; use the PWA fallback`;
  return null;
}

/**
 * Checks that a device can run the flows: a known profile and a supported WebView. Returns
 * the list of problems, empty when the device is fine.
 */
export function deviceProblems(code: string, userAgent: string): string[] {
  const problems: string[] = [];
  if (!findDeviceProfile(code)) problems.push(`unknown device profile ${code}`);
  const webView = checkWebView(webViewMajor(userAgent));
  if (webView) problems.push(webView);
  return problems;
}
