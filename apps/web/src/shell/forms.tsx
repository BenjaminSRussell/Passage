import { useMemo, useState, type ChangeEvent } from "react";
import type { EntityDef } from "./metadata";
import { validateEntity } from "./metadata";

export interface FormRendererProps {
  entity: EntityDef;
  values?: Record<string, string>;
  lines?: Record<string, string>[];
  onSubmit?: (values: Record<string, string>, lines: Record<string, string>[]) => void;
}

export function FormRenderer({ entity, values: initial, lines: initialLines, onSubmit }: FormRendererProps) {
  const [values, setValues] = useState<Record<string, string>>(initial ?? {});
  const [lines, setLines] = useState<Record<string, string>[]>(initialLines ?? []);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [editing, setEditing] = useState<{ row: number; field: string } | null>(null);

  const fields = entity.fields;
  const lineDef = entity.lines;

  const listRows = useMemo(() => (initialLines && initialLines.length ? initialLines : lines), [initialLines, lines]);

  function submit() {
    const next = validateEntity(entity, values);
    setErrors(next);
    if (Object.keys(next).length === 0) {
      onSubmit?.(values, lines);
    }
  }

  function updateLine(index: number, field: string, value: string) {
    setLines((current) => current.map((row, i) => (i === index ? { ...row, [field]: value } : row)));
  }

  return (
    <section aria-labelledby={`${entity.id}-heading`}>
      <h2 id={`${entity.id}-heading`}>{entity.title}</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {fields.map((field) => {
          const id = `${entity.id}-${field.name}`;
          const invalid = Boolean(errors[field.name]);
          const shared = {
            id,
            name: field.name,
            value: values[field.name] ?? "",
            "aria-invalid": invalid || undefined,
            "aria-describedby": invalid ? `${id}-error` : undefined,
            onChange: (e: ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>) =>
              setValues((v) => ({ ...v, [field.name]: e.target.value })),
          };
          return (
            <div key={field.name}>
              <label htmlFor={id}>{field.label}</label>
              {field.type === "text" ? (
                <textarea {...shared} />
              ) : field.type === "lookup" ? (
                <select {...shared}>
                  <option value="">Select</option>
                  {(field.lookup ?? []).map((opt) => (
                    <option key={opt} value={opt}>
                      {opt}
                    </option>
                  ))}
                </select>
              ) : (
                <input {...shared} type={field.type === "number" ? "number" : "text"} />
              )}
              {invalid ? (
                <p id={`${id}-error`} role="alert">
                  {errors[field.name]}
                </p>
              ) : null}
            </div>
          );
        })}
        <button type="submit">Save {entity.title}</button>
      </form>

      <table aria-label={`${entity.title} list`}>
        <thead>
          <tr>
            {fields.map((field) => (
              <th key={field.name} scope="col">
                {field.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {listRows.length === 0 ? (
            <tr>
              <td colSpan={fields.length}>No rows</td>
            </tr>
          ) : (
            listRows.map((row, index) => (
              <tr key={index}>
                {fields.map((field) => (
                  <td key={field.name}>
                    {editing?.row === index && editing.field === field.name ? (
                      <input
                        aria-label={`Edit ${field.label}`}
                        value={row[field.name] ?? ""}
                        onChange={(e) => updateLine(index, field.name, e.target.value)}
                        onBlur={() => setEditing(null)}
                        onKeyDown={(e) => {
                          if (e.key === "Enter" || e.key === "Escape") {
                            setEditing(null);
                          }
                        }}
                      />
                    ) : (
                      <button type="button" onClick={() => setEditing({ row: index, field: field.name })}>
                        {row[field.name] || `Edit ${field.label}`}
                      </button>
                    )}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>

      {lineDef ? (
        <section aria-labelledby={`${lineDef.id}-heading`}>
          <h3 id={`${lineDef.id}-heading`}>{lineDef.title}s</h3>
          <button type="button" onClick={() => setLines((current) => [...current, {}])}>
            Add {lineDef.title}
          </button>
          <table aria-label={`${lineDef.title} grid`}>
            <thead>
              <tr>
                {lineDef.fields.map((field) => (
                  <th key={field.name} scope="col">
                    {field.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {lines.map((row, index) => (
                <tr key={index}>
                  {lineDef.fields.map((field) => (
                    <td key={field.name}>
                      <label htmlFor={`${lineDef.id}-${index}-${field.name}`}>{field.label}</label>
                      <input
                        id={`${lineDef.id}-${index}-${field.name}`}
                        value={row[field.name] ?? ""}
                        onChange={(e) => updateLine(index, field.name, e.target.value)}
                      />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ) : null}
    </section>
  );
}
