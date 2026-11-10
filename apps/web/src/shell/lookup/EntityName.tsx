import { useEffect, useState } from "react";
import styles from "./EntityLookup.module.css";
import {
  formatOption,
  resolveOption,
  type LookupEntityKey,
  type LookupOption,
} from "./entities";

export function EntityName({
  entity,
  id,
  fallback,
}: {
  entity: LookupEntityKey;
  id: string;
  fallback?: string;
}) {
  const [entry, setEntry] = useState<LookupOption | null>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let live = true;
    setEntry(null);
    if (id !== "") {
      void resolveOption(entity, id).then((found) => {
        if (live) {
          setEntry(found);
        }
      });
    }
    return () => {
      live = false;
    };
  }, [entity, id]);

  if (id === "") {
    return <span>{fallback ?? "None"}</span>;
  }
  const shown = entry ? formatOption(entry) : (fallback ?? "Not available");

  async function copy() {
    try {
      await navigator.clipboard.writeText(id);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return (
    <span className={styles.name}>
      <span>{shown}</span>
      <details className={styles.details}>
        <summary aria-label={`Details for ${shown}`}>⋯</summary>
        <button type="button" onClick={() => void copy()}>
          Copy id
        </button>
        <span role="status">{copied ? "Copied" : ""}</span>
      </details>
    </span>
  );
}
