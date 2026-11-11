/** One server-sent event. */
export interface SseEvent {
  event: string;
  data: string;
}

/**
 * Reads server-sent events from a response body (the Anthropic Messages stream, FR-47.3). Frames are split on a
 * blank line; a frame without data is skipped, as the SSE specification says. An unterminated tail at the end of
 * the stream is not an event, so a truncated stream is visible to the caller as a missing terminal event.
 */
export async function* sseEvents(body: ReadableStream<Uint8Array>): AsyncGenerator<SseEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return;
      buffer += decoder.decode(value, { stream: true }).replace(/\r\n?/g, "\n");
      let cut = buffer.indexOf("\n\n");
      while (cut !== -1) {
        const frame = buffer.slice(0, cut);
        buffer = buffer.slice(cut + 2);
        const parsed = parseFrame(frame);
        if (parsed !== undefined) yield parsed;
        cut = buffer.indexOf("\n\n");
      }
    }
  } finally {
    reader.releaseLock();
  }
}

function parseFrame(frame: string): SseEvent | undefined {
  let event = "message";
  const data: string[] = [];
  for (const line of frame.split("\n")) {
    if (line === "" || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") event = value;
    else if (field === "data") data.push(value);
  }
  return data.length === 0 ? undefined : { event, data: data.join("\n") };
}

const SIMPLE_ESCAPES: Record<string, string> = { '"': '"', "\\": "\\", "/": "/", b: "\b", f: "\f", n: "\n", r: "\r", t: "\t" };

/**
 * Reads the top-level `text` string out of a JSON object that arrives in pieces (the `partial_json` of the forced
 * proposal tool call, whose `text` is the answer). `push` takes the next piece and returns the characters of the
 * value that are now complete, decoded, so the UI can show the answer as the model writes it. A `\u` escape or a
 * split character waits for its remaining digits. Keys of nested values are never mistaken for `text`.
 */
export class TextFieldExtractor {
  private depth = 0;
  private inString = false;
  private escaped = false;
  private unicode: string | undefined;
  private current = "";
  private expectKey = false;
  private readingKey = false;
  private lastKey = "";
  private afterColon = false;
  private streaming = false;
  private capturing = false;
  private toolText = "";
  private toolSeen = false;

  /** The value of the top-level `tool` key once its value has started: "" when it is empty or null, undefined before it is known. */
  get tool(): string | undefined {
    return this.toolSeen && !this.capturing ? this.toolText : undefined;
  }

  push(piece: string): string {
    let out = "";
    for (const ch of piece) {
      if (this.inString) {
        const decoded = this.stringChar(ch);
        if (decoded === null) {
          // the closing quote
          this.inString = false;
          if (this.readingKey) this.lastKey = this.current;
          this.readingKey = false;
          this.streaming = false;
          this.capturing = false;
          this.current = "";
        } else if (this.streaming) out += decoded;
        else if (this.capturing) this.toolText += decoded;
        else this.current += decoded;
        continue;
      }
      switch (ch) {
        case '"':
          this.inString = true;
          this.current = "";
          if (this.depth === 1 && this.expectKey) {
            this.readingKey = true;
            this.expectKey = false;
          } else if (this.depth === 1 && this.afterColon && this.lastKey === "text") {
            this.streaming = true;
          } else if (this.depth === 1 && this.afterColon && this.lastKey === "tool") {
            this.capturing = true;
            this.toolSeen = true;
          }
          this.afterColon = false;
          break;
        case "{":
        case "[":
          this.depth += 1;
          if (this.depth === 1) this.expectKey = true;
          this.afterColon = false;
          break;
        case "}":
        case "]":
          this.depth -= 1;
          break;
        case ",":
          if (this.depth === 1) this.expectKey = true;
          this.afterColon = false;
          break;
        case ":":
          if (this.depth === 1) this.afterColon = true;
          break;
        default:
          // a bare value (null) of `tool` is no tool
          if (this.depth === 1 && this.afterColon && this.lastKey === "tool" && ch !== " " && ch !== "\n" && ch !== "\t") {
            this.toolSeen = true;
            this.afterColon = false;
          }
      }
    }
    return out;
  }

  /** Decoded text of one string character, "" while an escape is incomplete, null at the closing quote. */
  private stringChar(ch: string): string | null {
    if (this.unicode !== undefined) {
      this.unicode += ch;
      if (this.unicode.length < 4) return "";
      const code = Number.parseInt(this.unicode, 16);
      this.unicode = undefined;
      return Number.isNaN(code) ? "" : String.fromCharCode(code);
    }
    if (this.escaped) {
      this.escaped = false;
      if (ch === "u") {
        this.unicode = "";
        return "";
      }
      return SIMPLE_ESCAPES[ch] ?? ch;
    }
    if (ch === "\\") {
      this.escaped = true;
      return "";
    }
    return ch === '"' ? null : ch;
  }
}

/**
 * Turns pseudonyms back into real values in streamed text. A pseudonym looks like `[IBAN-1]` and may arrive split
 * across pieces, so a piece is held back from an unclosed `[` until the pseudonym is complete or the text ends.
 */
const MAX_PSEUDONYM = 24;

export class StreamRestorer {
  private held = "";
  constructor(private readonly restore: (text: string) => string) {}

  push(piece: string): string {
    const text = this.held + piece;
    const open = text.lastIndexOf("[");
    // A real pseudonym is short; a stray "[" is not held past the longest one.
    if (open !== -1 && text.indexOf("]", open) === -1 && text.length - open <= MAX_PSEUDONYM) {
      this.held = text.slice(open);
      return this.restore(text.slice(0, open));
    }
    this.held = "";
    return this.restore(text);
  }

  flush(): string {
    const rest = this.held;
    this.held = "";
    return this.restore(rest);
  }
}
