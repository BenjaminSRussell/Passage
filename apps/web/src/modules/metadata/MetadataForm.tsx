import { FormEvent, useEffect, useState } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import styles from "./MetadataForm.module.css";

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

const LOADING_FIELDS = [0, 1, 2, 3];

export function MetadataForm({ entity }: MetadataFormProps) {
  const [schema, setSchema] = useState<EntitySchema>();
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setSchema(undefined);
    setError("");
    setSaved(false);
    fetch(`/api/v1/metadata/entities/${encodeURIComponent(entity)}/schema`, {
      signal: controller.signal,
      headers: apiHeaders(),
    })
      .then(async (response) => {
        if (response.status === 404) throw new Error(`No form is defined for ${entity} yet.`);
        if (!response.ok) throw new Error(`Schema request failed (${response.status})`);
        return (await response.json()) as EntitySchema;
      })
      .then(setSchema)
      .catch((reason: unknown) => {
        if (reason instanceof DOMException && reason.name === "AbortError") return;
        setError(reason instanceof Error ? reason.message : "Schema request failed");
      });
    return () => controller.abort();
  }, [entity, reload]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const formElement = event.currentTarget;
    setError("");
    setSaved(false);
    setSaving(true);
    const form = new FormData(formElement);
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
    try {
      const response = await fetch(`/api/v1/data/${encodeURIComponent(entity)}`, {
        method: "POST",
        headers: { ...apiHeaders(), "Content-Type": "application/json" },
        body: JSON.stringify(input),
      });
      if (!response.ok) {
        const problem = (await response.json().catch(() => ({}))) as { detail?: string };
        setError(problem.detail ?? `Save failed (${response.status})`);
        return;
      }
      setSaved(true);
      formElement.reset();
    } catch {
      setError("Save failed. Check your connection and try again.");
    } finally {
      setSaving(false);
    }
  }

  if (error && !schema) {
    return (
      <Alert
        tone="danger"
        live="alert"
        title="We couldn't load this form"
        action={
          <Button size="sm" onClick={() => setReload((count) => count + 1)}>
            Try again
          </Button>
        }
      >
        {error}
      </Alert>
    );
  }

  if (!schema) {
    return (
      <div aria-busy="true">
        <p aria-live="polite" className="visually-hidden">
          Loading {entity} metadata…
        </p>
        <Card>
          <div className={styles.fields}>
            {LOADING_FIELDS.map((key) => (
              <div key={key} className={styles.skeletonField}>
                <Skeleton width="1/4" />
                <Skeleton height="control" />
              </div>
            ))}
          </div>
        </Card>
      </div>
    );
  }

  const required = new Set(schema.required ?? []);
  return (
    <>
      <PageHeader title={schema.title} description="Fields come from the current tenant's effective metadata definition." />
      <Card>
        <form onChange={() => setSaved(false)} onSubmit={(event) => void submit(event)}>
          <div className={styles.fields}>
            {Object.entries(schema.properties)
              .filter(([, field]) => field["x-passage-visibility"] !== "hidden")
              .map(([name, field]) => (
                <FormField
                  key={name}
                  id={`metadata-${name}`}
                  label={field.title ?? humanize(name)}
                  optional={!required.has(name)}
                >
                  {(control) =>
                    field.enum ? (
                      <Select {...control} name={name} required={required.has(name)} defaultValue="">
                        <option value="" disabled>
                          Select…
                        </option>
                        {field.enum.map((value) => (
                          <option key={value} value={value}>
                            {value}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      <Input
                        {...control}
                        name={name}
                        type={inputType(field)}
                        required={required.has(name)}
                        readOnly={field.readOnly}
                        defaultValue={typeof field.default === "string" ? field.default : undefined}
                      />
                    )
                  }
                </FormField>
              ))}
          </div>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={saving}>
              Save {schema.title}
            </Button>
          </div>
        </form>
      </Card>
      <div className={styles.alerts}>
        <Alert tone="success" live="status">
          {saved ? `${schema.title} saved.` : null}
        </Alert>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
    </>
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
