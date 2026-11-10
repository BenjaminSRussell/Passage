import { localPreferenceStorage, type PreferenceStorage } from "./preferenceStorage";

export type Density = "comfortable" | "compact";

const densityKey = (identity: string) => `passage.density.v1.${identity}`;

export function readDensity(identity: string, storage: PreferenceStorage = localPreferenceStorage): Density {
  return identity !== "" && storage.read(densityKey(identity)) === "compact" ? "compact" : "comfortable";
}

export function writeDensity(identity: string, density: Density, storage: PreferenceStorage = localPreferenceStorage): void {
  if (identity !== "") {
    storage.write(densityKey(identity), density);
  }
}
