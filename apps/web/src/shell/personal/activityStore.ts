import { z } from "zod";
import { PinSchema, pinKey, type Pin, type PinTarget } from "../pins/pinStore";
import { localPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";
import { recentKey } from "../recent";

export const MAX_ACTIVITY = 50;

const ActivityList = z.array(PinSchema).max(MAX_ACTIVITY);

export type Activity = Pin;

export const activityKey = (identity: string) => `depot.activity.v1.${identity}`;

export function readActivity(identity: string, storage: PreferenceStorage = localPreferenceStorage): Activity[] {
  if (identity === "") return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(storage.read(activityKey(identity)) ?? "[]");
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  return parsed
    .flatMap((item) => {
      const result = PinSchema.safeParse(item);
      return result.success ? [result.data] : [];
    })
    .slice(0, MAX_ACTIVITY);
}

export function recordActivity(identity: string, target: PinTarget, storage: PreferenceStorage = localPreferenceStorage, now: () => Date = () => new Date()): void {
  if (identity === "") return;
  const rows = [{ ...target, at: now().toISOString() }, ...readActivity(identity, storage).filter((row) => pinKey(row) !== pinKey(target))].slice(0, MAX_ACTIVITY);
  const valid = ActivityList.safeParse(rows);
  if (valid.success) storage.write(activityKey(identity), JSON.stringify(valid.data));
}

export function resetPersonalization(identity: string, storage: PreferenceStorage = localPreferenceStorage): void {
  if (identity === "") return;
  storage.remove(activityKey(identity));
  storage.remove(recentKey(identity));
}
