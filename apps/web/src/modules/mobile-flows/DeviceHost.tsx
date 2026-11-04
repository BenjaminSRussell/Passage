import { DEFAULT_MENU_ID } from "./api/client";
import { DeviceApp } from "./DeviceApp";

/** Device settings provisioned on the handheld. Sign-in is a handoff (wave 2). */
function setting(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

/**
 * Handheld shell hosted by the web app for Playwright device emulation (AC-32.2, AC-32.4).
 * The production PWA is `apps/warehouse-mobile`, which renders the same DeviceApp.
 */
export function DeviceHost() {
  return (
    <DeviceApp
      deviceId={setting("passage.deviceId", "dev-unregistered")}
      userId={setting("passage.userId", "unassigned")}
      menuId={setting("passage.menuId", DEFAULT_MENU_ID)}
    />
  );
}
