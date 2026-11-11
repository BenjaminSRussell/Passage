import type { KeyValueStore, SealCipher } from "../../mobile-flows/domain/offlineStore";
import type { TechnicianDay } from "./types";

/** One row of the truck's stock as the device last saw it. */
export interface TruckStockItem {
  productId: string;
  unit: string;
  minQty: string;
  maxQty: string;
  onHandQty: string;
  inboundQty: string;
  short: boolean;
}

/** What the device keeps to work a day without a connection: the route, the customer names and the truck's stock. */
export interface DaySnapshot {
  day: TechnicianDay;
  names: Record<string, string>;
  products: Record<string, string>;
  truck: TruckStockItem[];
  savedAt: string;
}

const KEY = "technician-day";

export async function saveSnapshot(store: KeyValueStore<string>, cipher: SealCipher, snap: DaySnapshot): Promise<void> {
  await store.put(KEY, await cipher.seal(JSON.stringify(snap)));
}

export async function loadSnapshot(store: KeyValueStore<string>, cipher: SealCipher): Promise<DaySnapshot | null> {
  const sealed = await store.get(KEY);
  if (sealed === null) return null;
  try {
    return JSON.parse(await cipher.open(sealed)) as DaySnapshot;
  } catch {
    return null;
  }
}
