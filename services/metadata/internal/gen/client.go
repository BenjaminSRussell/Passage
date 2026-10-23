package gen

import (
	"fmt"
	"strings"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func tsClient(e *domain.Entity) string {
	n := e.Name
	var b strings.Builder
	omitted := []string{`"id"`, `"tenantId"`, `"version"`, `"ext"`}
	fmt.Fprintf(&b, "export interface %s {\n  id: string;\n  tenantId: string;\n", n)
	if e.PerLegalEntity {
		b.WriteString("  legalEntityId: string;\n")
		omitted = append(omitted, `"legalEntityId"`)
	}
	b.WriteString("  version: number;\n  ext: Record<string, unknown>;\n")
	for _, f := range e.Fields {
		for _, p := range props(e, f) {
			optional := ""
			if !f.Required {
				optional = "?"
			}
			fmt.Fprintf(&b, "  %s%s: %s;\n", p.Wire, optional, p.TS)
		}
	}
	fmt.Fprintf(&b, "}\n\nexport type %sInput = Omit<%s, %s>;\n\n", n, n, strings.Join(omitted, " | "))
	fmt.Fprintf(&b, "const base = \"/api%s\";\n\n", collectionPath(e))
	b.WriteString(`async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

`)
	fmt.Fprintf(&b, "export const list%[1]s = () => call<%[1]s[]>(\"\", \"GET\");\n", n)
	fmt.Fprintf(&b, "export const get%[1]s = (id: string) => call<%[1]s>(`/${id}`, \"GET\");\n", n)
	fmt.Fprintf(&b, "export const create%[1]s = (body: %[1]sInput, idempotencyKey: string) => call<%[1]s>(\"\", \"POST\", { \"Idempotency-Key\": idempotencyKey }, body);\n", n)
	fmt.Fprintf(&b, "export const update%[1]s = (id: string, body: Partial<%[1]sInput>, ifMatch: string) => call<%[1]s>(`/${id}`, \"PATCH\", { \"If-Match\": ifMatch }, body);\n", n)
	fmt.Fprintf(&b, "export const remove%[1]s = (id: string, ifMatch: string) => call<void>(`/${id}`, \"DELETE\", { \"If-Match\": ifMatch });\n", n)
	return b.String()
}
