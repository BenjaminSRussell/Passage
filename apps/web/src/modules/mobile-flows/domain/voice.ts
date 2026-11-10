// Voice adapter (FR-32.9). Pure: the speech engine is behind VoiceAdapter, so a vendor SDK (a
// Vocollect-style headset runtime or the Android speech service) is wired by the native shell and
// everything else, the grammar and the step routing, is tested here. A headset worker hears the
// step prompt, then says digits (a check string or a quantity) or a command; the utterance becomes
// the same input a keyboard or scanner would give the focused step.

import type { FlowStep } from "./flowGraph";

/** The speech engine: text to speech out, recognised text in. */
export interface VoiceAdapter {
  speak(text: string): void;
  /** Subscribes to recognised utterances. Returns the unsubscribe function. */
  onUtterance(handler: (text: string) => void): () => void;
}

export type VoiceAction =
  | { kind: "value"; value: string }
  | { kind: "confirm" }
  | { kind: "back" }
  | { kind: "repeat" }
  | { kind: "outcome"; word: "short" | "skip" }
  | { kind: "unknown"; heard: string };

const DIGIT_WORDS: Record<string, string> = {
  zero: "0",
  oh: "0",
  one: "1",
  two: "2",
  three: "3",
  tree: "3",
  four: "4",
  five: "5",
  fife: "5",
  six: "6",
  seven: "7",
  eight: "8",
  nine: "9",
  niner: "9",
};
const TEENS: Record<string, number> = {
  ten: 10,
  eleven: 11,
  twelve: 12,
  thirteen: 13,
  fourteen: 14,
  fifteen: 15,
  sixteen: 16,
  seventeen: 17,
  eighteen: 18,
  nineteen: 19,
};
const TENS: Record<string, number> = { twenty: 20, thirty: 30, forty: 40, fifty: 50, sixty: 60, seventy: 70, eighty: 80, ninety: 90 };

const CONFIRM = new Set(["ready", "confirm", "okay", "ok", "yes", "done", "next"]);
const BACK = new Set(["back", "previous", "go back"]);
const REPEAT = new Set(["repeat", "say again", "again"]);

/**
 * Reads spoken numbers: a run of digit words ("four oh two") is a digit string; number words
 * ("twelve", "forty five", "one hundred") are a quantity. Plain digits pass through. Returns
 * null when the text is not a number.
 */
export function spokenNumber(text: string): string | null {
  const words = text.toLowerCase().replace(/[,.]/g, " ").split(/\s+/).filter(Boolean);
  if (words.length === 0) return null;
  if (words.every((w) => /^\d+$/.test(w))) return words.join("");
  if (words.every((w) => w in DIGIT_WORDS)) return words.map((w) => DIGIT_WORDS[w]).join("");
  // Number words: [digit] hundred? [tens] [digit] | teens
  let total = 0;
  let current = 0;
  let seen = false;
  for (const w of words) {
    if (w in TEENS) current += TEENS[w];
    else if (w in TENS) current += TENS[w];
    else if (w in DIGIT_WORDS) current += Number(DIGIT_WORDS[w]);
    else if (w === "hundred" && current > 0 && current < 10) current *= 100;
    else if (w === "and" && seen) continue;
    else return null;
    seen = true;
  }
  total += current;
  return seen ? String(total) : null;
}

/** The grammar: commands first, then numbers. */
export function parseUtterance(text: string): VoiceAction {
  const heard = text.trim().toLowerCase().replace(/[.!?]/g, "");
  if (heard === "") return { kind: "unknown", heard: text };
  if (CONFIRM.has(heard)) return { kind: "confirm" };
  if (BACK.has(heard)) return { kind: "back" };
  if (REPEAT.has(heard)) return { kind: "repeat" };
  if (heard === "skip") return { kind: "outcome", word: "skip" };
  if (heard === "short" || heard === "short pick") return { kind: "outcome", word: "short" };
  const quantity = /^(?:quantity|count)\s+(.+)$/.exec(heard);
  const number = spokenNumber(quantity ? quantity[1] : heard);
  if (number !== null) return { kind: "value", value: number };
  return { kind: "unknown", heard: text };
}

/** What the device does with one utterance on the focused step. */
export type VoiceDecision =
  | { do: "value"; value: string }
  | { do: "scan"; raw: string }
  | { do: "confirm" }
  | { do: "outcome"; value: string }
  | { do: "back" }
  | { do: "say"; text: string };

/** The words a headset reads out for a step. */
export function promptFor(step: FlowStep): string {
  if (step.type === "branch") return `${step.prompt} Say ${Object.keys(step.next).join(", ")}.`;
  if (step.type === "confirm" || step.type === "display" || step.type === "call") return `${step.prompt} Say ready.`;
  return step.prompt;
}

/**
 * Routes an utterance to the focused step. Spoken digits answer a scan step as if they were
 * scanned (a check string) and an input or select step as a value; "ready" confirms; "short" and
 * "skip" pick the branch outcome of that name when the step has one.
 */
export function routeUtterance(text: string, step: FlowStep | undefined): VoiceDecision {
  if (!step) return { do: "say", text: "Nothing is waiting for an answer." };
  const action = parseUtterance(text);
  switch (action.kind) {
    case "back":
      return { do: "back" };
    case "repeat":
      return { do: "say", text: promptFor(step) };
    case "confirm":
      if (step.type === "confirm" || step.type === "display" || step.type === "call") return { do: "confirm" };
      if (step.type === "branch" && "ready" in step.next) return { do: "outcome", value: "ready" };
      return { do: "say", text: "Say the value first." };
    case "outcome":
      if (step.type === "branch" && action.word in step.next) return { do: "outcome", value: action.word };
      return { do: "say", text: `Not available on this step.` };
    case "value":
      if (step.type === "scan") return { do: "scan", raw: action.value };
      if (step.type === "input" || step.type === "select") return { do: "value", value: action.value };
      return { do: "say", text: promptFor(step) };
    default:
      return { do: "say", text: "Say again." };
  }
}
