// A tiny Prometheus text registry: counters with optional labels. No dependency.

export class Metrics {
  #counters = new Map<string, { help: string; values: Map<string, number> }>();

  inc(name: string, help: string, labels: Record<string, string> = {}, by = 1): void {
    const counter = this.#counters.get(name) ?? { help, values: new Map<string, number>() };
    const key = Object.entries(labels)
      .map(([k, v]) => `${k}="${v.replace(/[\\"\n]/g, "_")}"`)
      .join(",");
    counter.values.set(key, (counter.values.get(key) ?? 0) + by);
    this.#counters.set(name, counter);
  }

  render(): string {
    const lines: string[] = [];
    for (const [name, { help, values }] of this.#counters) {
      lines.push(`# HELP ${name} ${help}`, `# TYPE ${name} counter`);
      for (const [labels, value] of values) {
        lines.push(`${name}${labels === "" ? "" : `{${labels}}`} ${value}`);
      }
    }
    return `${lines.join("\n")}\n`;
  }
}
