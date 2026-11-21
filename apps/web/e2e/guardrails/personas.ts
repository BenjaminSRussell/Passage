export const DEV_IDENTITY = "00000000-0000-0000-0000-000000000001.dev";

export type PersonaName = "ap-clerk" | "planner" | "read-only";

export interface Persona {
  port: number;
  duties: readonly string[];
}

export const OPERATOR = {
  port: 4175,
  extraDuties: ["agents.admin", "agentbudget.read", "aipolicy.read"],
} as const;

export const PERSONAS: Record<PersonaName, Persona> = {
  "ap-clerk": {
    port: 4176,
    duties: ["mcp.copilot.use", "agents.run.read", "agents.invoke", "inbox.read", "receiving.vendor-invoice.post", "receiving.ap.inquire", "receiving.receipt.maintain"],
  },
  planner: {
    port: 4177,
    duties: ["mcp.copilot.use", "planning.read", "product.read", "production.order.read", "procurement.purchase-order.inquire"],
  },
  "read-only": {
    port: 4178,
    duties: ["metadata.read", "sales.order.read", "mcp.copilot.use"],
  },
};

export const personaNames = Object.keys(PERSONAS) as PersonaName[];

export const holds = (name: PersonaName, duty: string): boolean => PERSONAS[name].duties.includes(duty);
