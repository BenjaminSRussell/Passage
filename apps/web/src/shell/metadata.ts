/** Wave 3 seam: the shell renders from entity metadata and never imports services/metadata. */
export type FieldType = "string" | "number" | "lookup" | "text";

export interface FieldDef {
  name: string;
  label: string;
  type: FieldType;
  required?: boolean;
  lookup?: string[];
}

export interface EntityDef {
  id: string;
  title: string;
  fields: FieldDef[];
  lines?: EntityDef;
}

export interface MetadataCatalog {
  get(id: string): EntityDef | undefined;
  all(): EntityDef[];
}

export const demoNote: EntityDef = {
  id: "platform.note",
  title: "Note",
  fields: [
    { name: "title", label: "Title", type: "string", required: true },
    { name: "body", label: "Body", type: "text" },
    { name: "amount", label: "Amount", type: "number" },
    { name: "status", label: "Status", type: "lookup", lookup: ["draft", "open", "closed"] },
  ],
  lines: {
    id: "platform.noteLine",
    title: "Line",
    fields: [
      { name: "label", label: "Label", type: "string", required: true },
      { name: "qty", label: "Qty", type: "number", required: true },
    ],
  },
};

export function fakeCatalog(entities: EntityDef[] = [demoNote]): MetadataCatalog {
  const byId = new Map(entities.map((e) => [e.id, e]));
  return {
    get: (id) => byId.get(id),
    all: () => [...entities],
  };
}

export function validateEntity(def: EntityDef, values: Record<string, string>): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const field of def.fields) {
    const value = (values[field.name] ?? "").trim();
    if (field.required && value === "") {
      errors[field.name] = `${field.label} is required`;
    }
    if (field.type === "number" && value !== "" && Number.isNaN(Number(value))) {
      errors[field.name] = `${field.label} must be a number`;
    }
    if (field.type === "lookup" && value !== "" && field.lookup && !field.lookup.includes(value)) {
      errors[field.name] = `${field.label} is not a known value`;
    }
  }
  return errors;
}
