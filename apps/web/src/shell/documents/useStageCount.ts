import { useQuery } from "@tanstack/react-query";
import { holdsDuty } from "../dutyGate";
import { usePrincipalDuties } from "../principal";
import type { StageCount } from "./GuidedFlow";

/** A list that answered: how many rows it returned, and whether more exist beyond them. */
export interface Counted {
  count: number;
  more?: boolean;
}

/**
 * The count at one step of a guided flow. `duty` is the duty the list needs: without it the count is
 * "not available" and nothing is requested. A failed list is also "not available"; the step stays usable.
 */
export function useStageCount(key: string, noun: string, duty: string, load: () => Promise<Counted>): StageCount {
  const allowed = holdsDuty(usePrincipalDuties(), duty);
  const q = useQuery({ queryKey: ["guided-count", key], queryFn: load, enabled: allowed, retry: false });
  if (!allowed) return { noun, unavailable: true };
  if (q.isPending) return { noun, loading: true };
  if (q.isError) return { noun, unavailable: true };
  return { noun, value: `${q.data.count}${q.data.more ? "+" : ""}` };
}
