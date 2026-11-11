import type { DayVisit } from "./types";

/** A link that opens the visit's address in the device's maps app (Android and iOS both take it). */
export function navigateUrl(v: Pick<DayVisit, "latitude" | "longitude" | "address">): string | undefined {
  if (v.latitude != null && v.longitude != null) return `https://www.google.com/maps/dir/?api=1&destination=${v.latitude},${v.longitude}`;
  if (v.address.trim() !== "") return `https://www.google.com/maps/dir/?api=1&destination=${encodeURIComponent(v.address)}`;
  return undefined;
}

export interface Position {
  latitude: number;
  longitude: number;
}

/** The device's position for a signature, or undefined when it is not allowed or not available in a few seconds. */
export function currentPosition(timeoutMs = 4000): Promise<Position | undefined> {
  return new Promise((resolve) => {
    if (typeof navigator === "undefined" || !navigator.geolocation) return resolve(undefined);
    navigator.geolocation.getCurrentPosition(
      (p) => resolve({ latitude: Math.round(p.coords.latitude * 1e6) / 1e6, longitude: Math.round(p.coords.longitude * 1e6) / 1e6 }),
      () => resolve(undefined),
      { timeout: timeoutMs, maximumAge: 60_000 },
    );
  });
}
