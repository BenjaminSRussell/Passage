import { useEffect, useState } from "react";
import { formatOption, resolveOption, type LookupEntityKey } from "../../shell/lookup/entities";

/** Display names ("Name (Number)") for ids, for places that need plain text such as <option> labels. Ids show until resolved. */
export function useNames(entity: LookupEntityKey, ids: string[]): Record<string, string> {
  const [names, setNames] = useState<Record<string, string>>({});
  const key = [...new Set(ids)].sort().join("|");
  useEffect(() => {
    let live = true;
    for (const id of key.split("|").filter(Boolean)) {
      void resolveOption(entity, id).then((found) => {
        if (live && found) setNames((prev) => (prev[id] === formatOption(found) ? prev : { ...prev, [id]: formatOption(found) }));
      });
    }
    return () => {
      live = false;
    };
  }, [entity, key]);
  return names;
}
