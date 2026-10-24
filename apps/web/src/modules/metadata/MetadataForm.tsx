import { FormEvent, useEffect, useState } from "react";

interface FieldSchema {
  type?: string;
  title?: string;
  enum?: string[];
  format?: string;
  default?: unknown;
  readOnly?: boolean;
  "x-passage-visibility"?: "always" | "advanced" | "hidden";
}

interface EntitySchema {
  title: string;
  required?: string[];
  properties: Record<string, FieldSchema>;
}

export interface MetadataFormProps {
  entity: string;
}

export function MetadataForm({ entity }: MetadataFormProps) {
  const [schema, setSchema] = useState<EntitySchema>();
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    setSchema(undefined);
    setError("");
    fetch(`/api/v1/metadata/entities/${encodeURIComponent(entity)}/schema`, {
      signal: controller.signal,
      headers: apiHeaders(),
    })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Schema request failed (${response.status})`);
        return (await response.json()) as EntitySchema;
      })
      .then(setSchema)
      .catch((reason: unknown) => {
        if (reason instanceof DOMException && reason.name === "AbortError") return;
        setError(reason instanceof Error ? reason.message : "Schema request failed");
      });
    return () => controller.abort();
  }, [entity]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setSaved(false);
    const form = new FormData(event.currentTarget);
    const input: Record<string, unknown> = {};
    for (const [name, field] of Object.entries(schema?.properties ?? {})) {
      if (field.readOnly || field["x-passage-visibility"] === "hidden") continue;
      if (field.type === "boolean") {
        input[name] = form.get(name) === "on";
        continue;
      }
      const value = form.get(name);
      if (typeof value === "string" && value !== "") input[name] = value;
    }
    const response = await fetch(`/api/v1/data/${encodeURIComponent(entity)}`, {
      method: "POST",
      headers: { ...apiHeaders(), "Content-Type": "application/json" },
      body: JSON.stringify(input),
    });
    if (!response.ok) {
      const problem = (await response.json()) as { detail?: string };
      setError(problem.detail ?? `Save failed (${response.status})`);
      return;
    }
    setSaved(true);
    event.currentTarget.reset();
  }

  if (error && !schema) return <p role="alert">{error}</p>;
  if (!schema) return <p aria-live="polite">Loading {entity} metadata…</p>;

  const required = new Set(schema.required ?? []);
  return (
    <section aria-labelledby="metadata-form-title">
      <h1 id="metadata-form-title">{schema.title}</h1>
      <p>Fields come from the current tenant’s effective metadata definition.</p>
      <form onSubmit={(event) => void submit(event)}>
        {Object.entries(schema.properties)
          .filter(([, field]) => field["x-passage-visibility"] !== "hidden")
          .map(([name, field]) => (
            <div key={name}>
              <label htmlFor={`metadata-${name}`}>{field.title ?? humanize(name)}</label>
              {field.enum ? (
                <select id={`metadata-${name}`} name={name} required={required.has(name)} defaultValue="">
                  <option value="" disabled>
                    Select…
                  </option>
                  {field.enum.map((value) => (
                    <option key={value} value={value}>
                      {value}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  id={`metadata-${name}`}
                  name={name}
                  type={inputType(field)}
                  required={required.has(name)}
                  readOnly={field.readOnly}
                  defaultValue={typeof field.default === "string" ? field.default : undefined}
                />
              )}
            </div>
          ))}
        <button type="submit">Save</button>
      </form>
      {saved ? <p role="status">Saved.</p> : null}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}

function inputType(field: FieldSchema) {
  if (field.type === "boolean") return "checkbox";
  if (field.format === "date") return "date";
  if (field.format === "date-time") return "datetime-local";
  return "text";
}

function humanize(value: string) {
  return value.replace(/([A-Z])/g, " $1").replace(/^./, (letter) => letter.toUpperCase());
}

function apiHeaders(): Record<string, string> {
  if (!import.meta.env.DEV) return {};
  return {
    "X-Passage-Tenant": "00000000-0000-0000-0000-000000000001",
    "X-Passage-User": "00000000-0000-0000-0000-000000000002",
    "X-Passage-Duties": "metadata.read,metadata.write,metadata.data.read,metadata.data.write",
  };
}
