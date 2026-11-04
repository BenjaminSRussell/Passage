// CloudEvents SQL expressions for subscription filters (FR-35.9, §6.35.4): a
// subset of CESQL v1 over an event's context attributes, plus `data.<path>` to
// reach into the JSON payload. Pure: no I/O, no clock, no regular expressions
// built from input (LIKE is matched by a linear two-pointer walk).
//
//   expression  := or
//   or          := xor (OR xor)*                  xor := and (XOR and)*
//   and         := not (AND not)*                 not := NOT not | cmp
//   cmp         := sum ((= | != | <> | < | <= | > | >=) sum
//                       | [NOT] LIKE 'pattern' | [NOT] IN (sum, ...))?
//   sum         := term ((+ | -) term)*           term := unary ((* | / | %) unary)*
//   unary       := - unary | EXISTS attribute | primary
//   primary     := integer | 'string' | "string" | TRUE | FALSE | attribute
//                | function(args) | ( expression )
//
// Values are integers (32-bit, as in CESQL), strings and booleans. Operands are
// coerced to the other side's type as CESQL does ('12' = 12 is true); a value
// that cannot be coerced, a missing attribute, a divide by zero or an integer
// overflow is an evaluation error, and an event whose filter errors does not
// match (fail closed). Absent attributes are tested with EXISTS.

export const MAX_EXPRESSION_LENGTH = 1024;
const MAX_DEPTH = 32;
const MAX_NODES = 200;
const MAX_STRING_LENGTH = 65_536;
/**
 * Character steps one evaluation may spend in LIKE. The walk is linear in the text for most patterns but
 * O(text x pattern) for adversarial ones ('%aaaa...b' over a long run of a's); the consumer evaluates every
 * tenant's filters on one thread, so a pattern that would cost more than this does not match (fail closed).
 */
const MAX_LIKE_STEPS = 1_000_000;
const INT_MIN = -2_147_483_648;
const INT_MAX = 2_147_483_647;

/** The expression is not valid CESQL (or exceeds the limits); the message names the position. */
export class CesqlSyntaxError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "CesqlSyntaxError";
  }
}

/** An evaluation failed for this event (missing attribute, bad cast, overflow, divide by zero). */
export class CesqlEvalError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "CesqlEvalError";
  }
}

type Value = number | string | boolean;
type Kind = "int" | "string" | "bool" | "any";

type Node =
  | { t: "lit"; v: Value }
  | { t: "attr"; path: string[]; name: string }
  | { t: "exists"; path: string[]; name: string }
  | { t: "not" | "neg"; a: Node }
  | { t: "bin"; op: string; a: Node; b: Node }
  | { t: "like"; negate: boolean; a: Node; pattern: Node }
  | { t: "in"; negate: boolean; a: Node; list: Node[] }
  | { t: "fn"; name: string; args: Node[] };

interface Token {
  k: "int" | "str" | "word" | "op" | "end";
  v: string;
  pos: number;
}

const WORD = /[A-Za-z0-9_.]/;
const OPS = ["<>", "<=", ">=", "!=", "=", "<", ">", "+", "-", "*", "/", "%", "(", ")", ","];

function tokenize(src: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < src.length) {
    const c = src[i] as string;
    if (/\s/.test(c)) {
      i += 1;
    } else if (c === "'" || c === '"') {
      let j = i + 1;
      let s = "";
      for (;;) {
        if (j >= src.length) {
          throw new CesqlSyntaxError(`unterminated string at position ${i + 1}`);
        }
        const d = src[j] as string;
        // Only a quote or a backslash is unescaped here; any other backslash stays for LIKE (`\%` is a literal percent sign).
        if (d === "\\" && j + 1 < src.length && (src[j + 1] === c || src[j + 1] === "\\")) {
          s += src[j + 1];
          j += 2;
        } else if (d === c) {
          break;
        } else {
          s += d;
          j += 1;
        }
      }
      out.push({ k: "str", v: s, pos: i });
      i = j + 1;
    } else if (/[0-9]/.test(c)) {
      let j = i;
      while (j < src.length && /[0-9]/.test(src[j] as string)) j += 1;
      if (j < src.length && /[A-Za-z_.]/.test(src[j] as string)) {
        throw new CesqlSyntaxError(`bad number at position ${i + 1}`);
      }
      out.push({ k: "int", v: src.slice(i, j), pos: i });
      i = j;
    } else if (/[A-Za-z_]/.test(c)) {
      let j = i;
      while (j < src.length && WORD.test(src[j] as string)) j += 1;
      out.push({ k: "word", v: src.slice(i, j), pos: i });
      i = j;
    } else {
      const op = OPS.find((o) => src.startsWith(o, i));
      if (op === undefined) {
        throw new CesqlSyntaxError(`unexpected character ${JSON.stringify(c)} at position ${i + 1}`);
      }
      out.push({ k: "op", v: op, pos: i });
      i += op.length;
    }
  }
  out.push({ k: "end", v: "", pos: src.length });
  return out;
}

/** Function name to [min args, max args, result kind]. */
const FUNCTIONS: Record<string, [number, number, Kind]> = {
  LENGTH: [1, 1, "int"], CONCAT: [0, 20, "string"], CONCAT_WS: [2, 20, "string"], LOWER: [1, 1, "string"], UPPER: [1, 1, "string"],
  TRIM: [1, 1, "string"], LEFT: [2, 2, "string"], RIGHT: [2, 2, "string"], SUBSTRING: [2, 3, "string"], ABS: [1, 1, "int"],
  INT: [1, 1, "int"], STRING: [1, 1, "string"], BOOL: [1, 1, "bool"],
};
const KEYWORDS = new Set(["AND", "OR", "XOR", "NOT", "LIKE", "IN", "EXISTS", "TRUE", "FALSE"]);
/** Context attribute names (CloudEvents attribute names are lowercase letters and digits). */
const ATTRIBUTE = /^[a-z0-9]{1,20}$/;
const DATA_SEGMENT = /^[A-Za-z0-9_-]{1,64}$/;

class Parser {
  readonly #tokens: Token[];
  #i = 0;
  #nodes = 0;

  constructor(tokens: Token[]) {
    this.#tokens = tokens;
  }

  parse(): Node {
    const n = this.#or(0);
    if (this.#peek().k !== "end") {
      throw this.#fail(`unexpected ${this.#describe(this.#peek())}`);
    }
    return n;
  }

  #peek(): Token {
    return this.#tokens[this.#i] as Token;
  }

  #describe(t: Token): string {
    return t.k === "end" ? "end of expression" : `'${t.v}' at position ${t.pos + 1}`;
  }

  #fail(message: string): CesqlSyntaxError {
    return new CesqlSyntaxError(message);
  }

  #word(...words: string[]): boolean {
    const t = this.#peek();
    return t.k === "word" && words.includes(t.v.toUpperCase());
  }

  #op(...ops: string[]): boolean {
    const t = this.#peek();
    return t.k === "op" && ops.includes(t.v);
  }

  #expectOp(op: string): void {
    if (!this.#op(op)) {
      throw this.#fail(`expected '${op}' but found ${this.#describe(this.#peek())}`);
    }
    this.#i += 1;
  }

  #node<T extends Node>(n: T, depth: number): T {
    this.#nodes += 1;
    if (this.#nodes > MAX_NODES) {
      throw this.#fail(`expression has more than ${MAX_NODES} terms`);
    }
    if (depth > MAX_DEPTH) {
      throw this.#fail(`expression is nested deeper than ${MAX_DEPTH} levels`);
    }
    return n;
  }

  /** Checked on the way down, before recursing, so a deep nesting is refused before it can exhaust the stack. */
  #descend(depth: number): void {
    if (depth > MAX_DEPTH) {
      throw this.#fail(`expression is nested deeper than ${MAX_DEPTH} levels`);
    }
  }

  #or(depth: number): Node {
    this.#descend(depth);
    let a = this.#xor(depth);
    while (this.#word("OR")) {
      this.#i += 1;
      a = this.#node({ t: "bin", op: "OR", a, b: this.#xor(depth) }, depth);
    }
    return a;
  }

  #xor(depth: number): Node {
    let a = this.#and(depth);
    while (this.#word("XOR")) {
      this.#i += 1;
      a = this.#node({ t: "bin", op: "XOR", a, b: this.#and(depth) }, depth);
    }
    return a;
  }

  #and(depth: number): Node {
    let a = this.#not(depth);
    while (this.#word("AND")) {
      this.#i += 1;
      a = this.#node({ t: "bin", op: "AND", a, b: this.#not(depth) }, depth);
    }
    return a;
  }

  #not(depth: number): Node {
    this.#descend(depth);
    if (this.#word("NOT")) {
      this.#i += 1;
      return this.#node({ t: "not", a: this.#not(depth + 1) }, depth);
    }
    return this.#cmp(depth);
  }

  #cmp(depth: number): Node {
    const a = this.#sum(depth);
    const t = this.#peek();
    if (t.k === "op" && ["=", "!=", "<>", "<", "<=", ">", ">="].includes(t.v)) {
      this.#i += 1;
      return this.#node({ t: "bin", op: t.v === "<>" ? "!=" : t.v, a, b: this.#sum(depth) }, depth);
    }
    let negate = false;
    if (this.#word("NOT")) {
      negate = true;
      this.#i += 1;
      if (!this.#word("LIKE", "IN")) {
        throw this.#fail(`expected LIKE or IN after NOT but found ${this.#describe(this.#peek())}`);
      }
    }
    if (this.#word("LIKE")) {
      this.#i += 1;
      const pattern = this.#sum(depth);
      if (pattern.t !== "lit" || typeof pattern.v !== "string") {
        throw this.#fail("LIKE needs a quoted pattern");
      }
      return this.#node({ t: "like", negate, a, pattern }, depth);
    }
    if (this.#word("IN")) {
      this.#i += 1;
      this.#expectOp("(");
      const list: Node[] = [this.#sum(depth + 1)];
      while (this.#op(",")) {
        this.#i += 1;
        list.push(this.#sum(depth + 1));
      }
      this.#expectOp(")");
      return this.#node({ t: "in", negate, a, list }, depth);
    }
    return a;
  }

  #sum(depth: number): Node {
    let a = this.#term(depth);
    while (this.#op("+", "-")) {
      const op = this.#peek().v;
      this.#i += 1;
      a = this.#node({ t: "bin", op, a, b: this.#term(depth) }, depth);
    }
    return a;
  }

  #term(depth: number): Node {
    let a = this.#unary(depth);
    while (this.#op("*", "/", "%")) {
      const op = this.#peek().v;
      this.#i += 1;
      a = this.#node({ t: "bin", op, a, b: this.#unary(depth) }, depth);
    }
    return a;
  }

  #unary(depth: number): Node {
    this.#descend(depth);
    if (this.#op("-")) {
      this.#i += 1;
      return this.#node({ t: "neg", a: this.#unary(depth + 1) }, depth);
    }
    if (this.#word("EXISTS")) {
      this.#i += 1;
      const t = this.#peek();
      if (t.k !== "word" || KEYWORDS.has(t.v.toUpperCase())) {
        throw this.#fail(`EXISTS needs an attribute name but found ${this.#describe(t)}`);
      }
      this.#i += 1;
      return this.#node({ t: "exists", ...this.#attribute(t) }, depth);
    }
    return this.#primary(depth);
  }

  #attribute(t: Token): { path: string[]; name: string } {
    const parts = t.v.split(".");
    if (parts[0] === "data") {
      if (parts.length < 2 || parts.length > 12 || parts.slice(1).some((p) => !DATA_SEGMENT.test(p))) {
        throw this.#fail(`'${t.v}' at position ${t.pos + 1} is not a data path such as data.net_total`);
      }
      return { path: parts.slice(1), name: t.v };
    }
    if (parts.length !== 1 || !ATTRIBUTE.test(t.v)) {
      throw this.#fail(`'${t.v}' at position ${t.pos + 1} is not an attribute name (lowercase letters and digits) or a data path`);
    }
    return { path: [], name: t.v };
  }

  #primary(depth: number): Node {
    const t = this.#peek();
    if (t.k === "int") {
      this.#i += 1;
      const n = Number(t.v);
      if (n > INT_MAX) {
        throw this.#fail(`integer ${t.v} at position ${t.pos + 1} does not fit in 32 bits`);
      }
      return this.#node({ t: "lit", v: n }, depth);
    }
    if (t.k === "str") {
      this.#i += 1;
      if (t.v.length > MAX_STRING_LENGTH) {
        throw this.#fail("string literal is too long");
      }
      return this.#node({ t: "lit", v: t.v }, depth);
    }
    if (t.k === "op" && t.v === "(") {
      this.#i += 1;
      const inner = this.#or(depth + 1);
      this.#expectOp(")");
      return inner;
    }
    if (t.k === "word") {
      const upper = t.v.toUpperCase();
      if (upper === "TRUE" || upper === "FALSE") {
        this.#i += 1;
        return this.#node({ t: "lit", v: upper === "TRUE" }, depth);
      }
      if (KEYWORDS.has(upper)) {
        throw this.#fail(`unexpected ${this.#describe(t)}`);
      }
      this.#i += 1;
      const next = this.#peek();
      if (next.k === "op" && next.v === "(") {
        const spec = FUNCTIONS[upper];
        if (spec === undefined) {
          throw this.#fail(`unknown function ${t.v} at position ${t.pos + 1}`);
        }
        this.#i += 1;
        const args: Node[] = [];
        if (!this.#op(")")) {
          args.push(this.#or(depth + 1));
          while (this.#op(",")) {
            this.#i += 1;
            args.push(this.#or(depth + 1));
          }
        }
        this.#expectOp(")");
        if (args.length < spec[0] || args.length > spec[1]) {
          throw this.#fail(`${upper} takes ${spec[0] === spec[1] ? spec[0] : `${spec[0]} to ${spec[1]}`} argument(s), got ${args.length}`);
        }
        return this.#node({ t: "fn", name: upper, args }, depth);
      }
      return this.#node({ t: "attr", ...this.#attribute(t) }, depth);
    }
    throw this.#fail(`unexpected ${this.#describe(t)}`);
  }
}

/** The static kind of a node, to refuse a filter that can never be true or false (such as `1 + 2`). */
function kindOf(n: Node): Kind {
  switch (n.t) {
    case "lit":
      return typeof n.v === "number" ? "int" : typeof n.v === "string" ? "string" : "bool";
    case "attr":
      return "any";
    case "exists":
    case "not":
    case "like":
    case "in":
      return "bool";
    case "neg":
      return "int";
    case "fn":
      return (FUNCTIONS[n.name] as [number, number, Kind])[2];
    case "bin":
      return ["+", "-", "*", "/", "%"].includes(n.op) ? "int" : "bool";
  }
}

export interface CompiledFilter {
  readonly source: string;
  /** True when the expression holds for the event; false when it does not or cannot be evaluated. */
  matches(event: FilterEvent): boolean;
}

/** What an expression sees: the context attributes by name and the event's JSON data. */
export interface FilterEvent {
  attributes: Readonly<Record<string, string | number | boolean | undefined>>;
  data: unknown;
}

/** Parses and checks an expression; throws CesqlSyntaxError. */
export function compileFilter(source: string): CompiledFilter {
  if (source.trim() === "") {
    throw new CesqlSyntaxError("the filter is empty");
  }
  if (source.length > MAX_EXPRESSION_LENGTH) {
    throw new CesqlSyntaxError(`the filter is longer than ${MAX_EXPRESSION_LENGTH} characters`);
  }
  const ast = new Parser(tokenize(source)).parse();
  const kind = kindOf(ast);
  if (kind !== "bool" && kind !== "any") {
    throw new CesqlSyntaxError(`the filter must be a true or false condition, not ${kind === "int" ? "a number" : "text"}`);
  }
  return {
    source,
    matches(event) {
      likeSteps = MAX_LIKE_STEPS;
      try {
        return toBool(evaluate(ast, event));
      } catch (err) {
        if (err instanceof CesqlEvalError) {
          return false;
        }
        throw err;
      }
    },
  };
}

/** Returns null when the expression is valid, otherwise the message for the caller. */
export function validateFilter(source: string): string | null {
  try {
    compileFilter(source);
    return null;
  } catch (err) {
    if (err instanceof CesqlSyntaxError) {
      return err.message;
    }
    throw err;
  }
}

const cache = new Map<string, CompiledFilter>();
const CACHE_SIZE = 500;

/** compileFilter with a small cache: the consumer evaluates the same few expressions for every event. */
export function filterFor(source: string): CompiledFilter {
  let f = cache.get(source);
  if (f === undefined) {
    f = compileFilter(source);
    if (cache.size >= CACHE_SIZE) {
      cache.delete(cache.keys().next().value as string);
    }
    cache.set(source, f);
  }
  return f;
}

function lookup(n: { path: string[]; name: string }, e: FilterEvent): Value | undefined {
  if (n.path.length === 0) {
    const v = e.attributes[n.name];
    return v === undefined ? undefined : v;
  }
  let cur: unknown = e.data;
  for (const seg of n.path) {
    if (typeof cur !== "object" || cur === null || Array.isArray(cur) || !Object.hasOwn(cur, seg)) {
      return undefined;
    }
    cur = (cur as Record<string, unknown>)[seg];
  }
  if (typeof cur === "string" || typeof cur === "boolean") {
    return cur;
  }
  if (typeof cur === "number" && Number.isInteger(cur)) {
    return cur;
  }
  // null, objects, arrays and fractional numbers are not CESQL values; a decimal is a string in Passage events.
  return undefined;
}

function toInt(v: Value): number {
  if (typeof v === "number") return v;
  if (typeof v === "string" && /^[+-]?[0-9]{1,10}$/.test(v.trim())) {
    const n = Number(v.trim());
    if (n >= INT_MIN && n <= INT_MAX) return n;
  }
  throw new CesqlEvalError(`cannot read ${JSON.stringify(v)} as an integer`);
}

function toBool(v: Value): boolean {
  if (typeof v === "boolean") return v;
  if (typeof v === "string") {
    const s = v.toLowerCase();
    if (s === "true") return true;
    if (s === "false") return false;
  }
  throw new CesqlEvalError(`cannot read ${JSON.stringify(v)} as a boolean`);
}

function toStr(v: Value): string {
  return typeof v === "string" ? v : String(v);
}

function checked(n: number): number {
  if (!Number.isInteger(n) || n < INT_MIN || n > INT_MAX) {
    throw new CesqlEvalError("integer overflow");
  }
  return n;
}

/** Two values compared in the type of the right-hand side when the left is an attribute, as CESQL does; here the literal side wins. */
function compare(op: string, a: Value, b: Value): boolean {
  let x: Value = a;
  let y: Value = b;
  if (typeof a !== typeof b) {
    // Coerce to the type of the side that is not an attribute-typed string; a number or boolean wins over a string.
    if (typeof a === "string") {
      x = typeof b === "number" ? toInt(a) : toBool(a);
    } else if (typeof b === "string") {
      y = typeof a === "number" ? toInt(b) : toBool(b);
    } else {
      throw new CesqlEvalError("cannot compare a number with a boolean");
    }
  }
  switch (op) {
    case "=":
      return x === y;
    case "!=":
      return x !== y;
  }
  if (typeof x === "boolean" || typeof y === "boolean") {
    throw new CesqlEvalError("booleans cannot be ordered");
  }
  switch (op) {
    case "<":
      return x < y;
    case "<=":
      return x <= y;
    case ">":
      return x > y;
    default:
      return x >= y;
  }
}

/** LIKE steps left in the current evaluation (evaluation is synchronous, so one counter serves every filter). */
let likeSteps = MAX_LIKE_STEPS;

/**
 * SQL LIKE: % is any run of characters, _ one character, \ escapes either. A walk with one backtrack point (no
 * regular expression, no exponential case); each step is charged to the evaluation's budget (MAX_LIKE_STEPS).
 */
function like(text: string, pattern: string): boolean {
  const toks: { any: boolean; one: boolean; ch: string }[] = [];
  for (let i = 0; i < pattern.length; i += 1) {
    const c = pattern[i] as string;
    if (c === "\\" && i + 1 < pattern.length) {
      i += 1;
      toks.push({ any: false, one: false, ch: pattern[i] as string });
    } else {
      toks.push({ any: c === "%", one: c === "_", ch: c });
    }
  }
  let t = 0;
  let p = 0;
  let star = -1;
  let mark = 0;
  while (t < text.length) {
    likeSteps -= 1;
    if (likeSteps < 0) {
      throw new CesqlEvalError("LIKE pattern is too costly for this value");
    }
    const tok = toks[p];
    if (tok !== undefined && !tok.any && (tok.one || tok.ch === text[t])) {
      t += 1;
      p += 1;
    } else if (tok !== undefined && tok.any) {
      star = p;
      mark = t;
      p += 1;
    } else if (star >= 0) {
      p = star + 1;
      mark += 1;
      t = mark;
    } else {
      return false;
    }
  }
  while (toks[p]?.any === true) p += 1;
  return p === toks.length;
}

function evaluate(n: Node, e: FilterEvent): Value {
  switch (n.t) {
    case "lit":
      return n.v;
    case "attr": {
      const v = lookup(n, e);
      if (v === undefined) {
        throw new CesqlEvalError(`${n.name} is missing`);
      }
      return v;
    }
    case "exists":
      return lookup(n, e) !== undefined;
    case "not":
      return !toBool(evaluate(n.a, e));
    case "neg":
      return checked(-toInt(evaluate(n.a, e)));
    case "like": {
      const hit = like(toStr(evaluate(n.a, e)), (n.pattern as { v: string }).v);
      return n.negate ? !hit : hit;
    }
    case "in": {
      const a = evaluate(n.a, e);
      let hit = false;
      for (const item of n.list) {
        if (compare("=", a, evaluate(item, e))) {
          hit = true;
          break;
        }
      }
      return n.negate ? !hit : hit;
    }
    case "bin":
      return binary(n, e);
    case "fn":
      return call(n.name, n.args.map((a) => evaluate(a, e)));
  }
}

function binary(n: { op: string; a: Node; b: Node }, e: FilterEvent): Value {
  // AND, OR and XOR evaluate both sides: an error on either side is an error, not a silent short circuit.
  const a = evaluate(n.a, e);
  const b = evaluate(n.b, e);
  switch (n.op) {
    case "AND":
      return toBool(a) && toBool(b);
    case "OR":
      return toBool(a) || toBool(b);
    case "XOR":
      return toBool(a) !== toBool(b);
    case "+":
      return checked(toInt(a) + toInt(b));
    case "-":
      return checked(toInt(a) - toInt(b));
    case "*":
      return checked(toInt(a) * toInt(b));
    case "/":
    case "%": {
      const d = toInt(b);
      if (d === 0) {
        throw new CesqlEvalError("division by zero");
      }
      return checked(n.op === "/" ? Math.trunc(toInt(a) / d) : toInt(a) % d);
    }
    default:
      return compare(n.op, a, b);
  }
}

function call(name: string, a: Value[]): Value {
  const s = (i: number): string => toStr(a[i] as Value);
  const guard = (v: string): string => {
    if (v.length > MAX_STRING_LENGTH) {
      throw new CesqlEvalError("string too long");
    }
    return v;
  };
  switch (name) {
    case "LENGTH":
      return s(0).length;
    case "CONCAT":
      return guard(a.map(toStr).join(""));
    case "CONCAT_WS":
      return guard(a.slice(1).map(toStr).join(s(0)));
    case "LOWER":
      return s(0).toLowerCase();
    case "UPPER":
      return s(0).toUpperCase();
    case "TRIM":
      return s(0).trim();
    case "LEFT": {
      const n = toInt(a[1] as Value);
      return n < 0 ? "" : s(0).slice(0, n);
    }
    case "RIGHT": {
      const n = toInt(a[1] as Value);
      return n <= 0 ? "" : s(0).slice(-n);
    }
    case "SUBSTRING": {
      // SQL positions start at 1; a negative position counts from the end.
      const text = s(0);
      const pos = toInt(a[1] as Value);
      const start = pos > 0 ? pos - 1 : pos < 0 ? Math.max(text.length + pos, 0) : 0;
      if (a.length === 3) {
        const len = toInt(a[2] as Value);
        return len <= 0 ? "" : text.slice(start, start + len);
      }
      return text.slice(start);
    }
    case "ABS":
      return checked(Math.abs(toInt(a[0] as Value)));
    case "INT":
      return toInt(a[0] as Value);
    case "STRING":
      return s(0);
    default:
      return toBool(a[0] as Value);
  }
}
