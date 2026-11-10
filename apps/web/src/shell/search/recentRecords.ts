import { localPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";
import { isAppPath, RECORD_TYPES, type RecordHit } from "./records";

export const MAX_RECENT = 8;
export const MAX_FAVOURITES = 20;

const recentKey = (identity: string) => `passage.records.recent.v1.${identity}`;
const favouriteKey = (identity: string) => `passage.records.favourites.v1.${identity}`;

const sameRecord = (a: RecordHit, b: RecordHit) => a.type === b.type && a.id === b.id;

function isHit(value: unknown): value is RecordHit {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const r = value as Partial<Record<keyof RecordHit, unknown>>;
  return (
    typeof r.type === "string" &&
    (RECORD_TYPES as readonly string[]).includes(r.type) &&
    typeof r.id === "string" &&
    typeof r.number === "string" &&
    typeof r.title === "string" &&
    typeof r.link === "string" &&
    isAppPath(r.link)
  );
}

function parse(raw: string | null): RecordHit[] {
  if (raw === null) {
    return [];
  }
  try {
    const value: unknown = JSON.parse(raw);
    return Array.isArray(value) ? value.filter(isHit) : [];
  } catch {
    return [];
  }
}

/** What a user opened lately, newest first. Kept in this browser, per user. */
export function readRecent(identity: string, storage: PreferenceStorage = localPreferenceStorage): RecordHit[] {
  return identity === "" ? [] : parse(storage.read(recentKey(identity))).slice(0, MAX_RECENT);
}

export function pushRecent(identity: string, hit: RecordHit, storage: PreferenceStorage = localPreferenceStorage): void {
  if (identity === "") {
    return;
  }
  const next = [hit, ...readRecent(identity, storage).filter((r) => !sameRecord(r, hit))].slice(0, MAX_RECENT);
  storage.write(recentKey(identity), JSON.stringify(next));
}

export function readFavourites(identity: string, storage: PreferenceStorage = localPreferenceStorage): RecordHit[] {
  return identity === "" ? [] : parse(storage.read(favouriteKey(identity))).slice(0, MAX_FAVOURITES);
}

export function isFavourite(favourites: RecordHit[], hit: RecordHit): boolean {
  return favourites.some((f) => sameRecord(f, hit));
}

/** Adds the record to the favourites, or removes it when it is already there. Returns the new list. */
export function toggleFavourite(identity: string, hit: RecordHit, storage: PreferenceStorage = localPreferenceStorage): RecordHit[] {
  const current = readFavourites(identity, storage);
  const next = isFavourite(current, hit) ? current.filter((f) => !sameRecord(f, hit)) : [hit, ...current].slice(0, MAX_FAVOURITES);
  if (identity !== "") {
    storage.write(favouriteKey(identity), JSON.stringify(next));
  }
  return next;
}
