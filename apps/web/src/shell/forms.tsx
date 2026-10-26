import { useEffect, useMemo, useRef, useState, type ChangeEvent } from "react";
import styles from "./forms.module.css";
import type { EntityDef } from "./metadata";
import { validateEntity } from "./metadata";
import { Button, FormField, Input, Select, Textarea } from "./ui";

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
  const editRef = useRef<HTMLInputElement>(null);

  const fields = entity.fields;
  const lineDef = entity.lines;

  const listRows = useMemo(() => (initialLines && initialLines.length ? initialLines : lines), [initialLines, lines]);

  useEffect(() => {
    if (editing) {
      editRef.current?.focus();
    }
  }, [editing]);

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
    <section aria-labelledby={`${entity.id}-heading`} className={styles.form}>
      <h2 id={`${entity.id}-heading`} className={styles.heading}>
        {entity.title}
      </h2>
      <form
        className={styles.fields}
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {fields.map((field) => {
          const id = `${entity.id}-${field.name}`;
          const shared = {
            name: field.name,
            value: values[field.name] ?? "",
            invalid: Boolean(errors[field.name]),
            onChange: (e: ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>) =>
              setValues((v) => ({ ...v, [field.name]: e.target.value })),
          };
          return (
            <FormField key={field.name} id={id} label={field.label} optional={!field.required} error={errors[field.name]}>
              {(control) =>
                field.type === "text" ? (
                  <Textarea {...control} {...shared} />
                ) : field.type === "lookup" ? (
                  <Select {...control} {...shared}>
                    <option value="">Select</option>
                    {(field.lookup ?? []).map((opt) => (
                      <option key={opt} value={opt}>
                        {opt}
                      </option>
                    ))}
                  </Select>
                ) : (
                  <Input {...control} {...shared} type={field.type === "number" ? "number" : "text"} />
                )
              }
            </FormField>
          );
        })}
        <div className={styles.actions}>
          <Button type="submit" variant="primary">
            Save {entity.title}
          </Button>
        </div>
      </form>

      <table aria-label={`${entity.title} list`} className={styles.table}>
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
              <td colSpan={fields.length} className={styles.empty}>
                No rows
              </td>
            </tr>
          ) : (
            listRows.map((row, index) => (
              <tr key={index}>
                {fields.map((field) => (
                  <td key={field.name}>
                    {editing?.row === index && editing.field === field.name ? (
                      <Input
                        ref={editRef}
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
                      <button type="button" className={styles.cellButton} onClick={() => setEditing({ row: index, field: field.name })}>
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
        <section aria-labelledby={`${lineDef.id}-heading`} className={styles.lines}>
          <div className={styles.linesHead}>
            <h3 id={`${lineDef.id}-heading`} className={styles.subheading}>
              {lineDef.title}s
            </h3>
            <Button size="sm" onClick={() => setLines((current) => [...current, {}])}>
              Add {lineDef.title}
            </Button>
          </div>
          <table aria-label={`${lineDef.title} grid`} className={styles.table}>
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
                      <label htmlFor={`${lineDef.id}-${index}-${field.name}`} className="visually-hidden">
                        {field.label}
                      </label>
                      <Input
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
