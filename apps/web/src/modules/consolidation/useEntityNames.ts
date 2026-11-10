import { useEffect, useState } from "react";
import { formatOption, resolveOption } from "../../shell/lookup/entities";

/** Names of legal entities by id, for places a plain text label is needed (select options, table headers). */
export function useLegalEntityNames(ids: string[]): Record<string, string> {
  const [names, setNames] = useState<Record<string, string>>({});
  const key = [...new Set(ids)].sort().join(",");
  useEffect(() => {
    let live = true;
    for (const id of key === "" ? [] : key.split(",")) {
      void resolveOption("legalEntity", id).then((found) => {
        if (live && found) setNames((prev) => ({ ...prev, [id]: formatOption(found) }));
      });
    }
    return () => {
      live = false;
    };
  }, [key]);
  return names;
}
