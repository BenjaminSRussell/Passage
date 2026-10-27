// Layout rules for the device app (FR-32.1, §6.32.6, §6.32.20). Pure: the app's CSS uses the
// same numbers, and these functions let tests check them without a browser.

/** Narrowest supported screen (§6.32.6 "supports screen widths from 320 px"). */
export const MIN_VIEWPORT_PX = 320;
/** Smallest readable text on a handheld (§6.32.6 "font ≥ 16 px"). */
export const MIN_FONT_PX = 16;
/** Standard touch target and the large-target mode used with gloves (FR-32.1 notes). */
export const TARGET_PX = 48;
export const LARGE_TARGET_PX = 64;
export const LARGE_TARGET_FONT_SCALE = 1.25;

const RTL_LANGUAGES = new Set(["ar", "he", "fa", "ur"]);

/** Reading direction for a BCP 47 locale such as "ar-SA" or "en-US". */
export function textDirection(locale: string): "rtl" | "ltr" {
  const language = locale.trim().toLowerCase().split(/[-_]/)[0] ?? "";
  return RTL_LANGUAGES.has(language) ? "rtl" : "ltr";
}

/** Font size for a base size, scaled in large-target mode, never below MIN_FONT_PX. */
export function fontSizePx(basePx: number, largeTargets: boolean): number {
  const scaled = largeTargets ? Math.round(basePx * LARGE_TARGET_FONT_SCALE) : basePx;
  return Math.max(MIN_FONT_PX, scaled);
}

/** Minimum touch target edge for the current mode. */
export function targetSizePx(largeTargets: boolean): number {
  return largeTargets ? LARGE_TARGET_PX : TARGET_PX;
}

/** Whether a viewport width is inside the supported range. */
export function fitsViewport(widthPx: number): boolean {
  return Number.isFinite(widthPx) && widthPx >= MIN_VIEWPORT_PX;
}
