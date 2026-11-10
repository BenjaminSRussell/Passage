/**
 * The strings in `shell/copy.ts` that are functions, written as ICU messages. English keeps using its function, so
 * nothing changes for English; other languages format their ICU message with the same arguments. A test checks that
 * each ICU message here gives exactly what the English function gives, so the two cannot drift.
 *
 * `params` names the function's arguments in order. A boolean argument is passed to ICU as "true" or "false".
 * `samples` are argument lists the consistency test tries.
 */
export interface FunctionMessage {
  params: readonly string[];
  en: string;
  samples: readonly (readonly unknown[])[];
}

const n = [[0], [2], [5]] as const;
const q = [["hose"]] as const;
const label = [["Reports"]] as const;
const noun = [["orders"]] as const;
const countNoun = [[3, "orders"], [1, "orders"]] as const;

export const FUNCTION_MESSAGES: Readonly<Record<string, FunctionMessage>> = {
  "allFeatures.count": { params: ["count"], en: "{count, plural, one {# feature} other {# features}}", samples: [[0], [1], [2], [5]] },
  "allFeatures.noMatches": { params: ["query"], en: "No features match “{query}”.", samples: q },
  "announce.results": { params: ["count"], en: "{count} results", samples: n },
  "announce.opened": { params: ["label"], en: "Opened {label}", samples: label },
  "announce.workspaceSwitched": { params: ["label"], en: "Workspace changed to {label}", samples: label },
  "forms.errorSummaryTitle": {
    params: ["count"],
    en: "{count, plural, one {# thing to fix before you can save} other {# things to fix before you can save}}",
    samples: [[1], [2], [4]],
  },
  "lists.removeFilter": { params: ["label"], en: "Remove filter {label}", samples: label },
  "lists.updateView": { params: ["name"], en: "Update {name}", samples: label },
  "lists.deleteView": { params: ["name"], en: "Delete {name}", samples: label },
  "lists.applyView": { params: ["name"], en: "Show {name}", samples: label },
  "lists.makeDefault": { params: ["name"], en: "Make {name} the default", samples: label },
  "lists.clearDefault": { params: ["name"], en: "Stop using {name} as the default", samples: label },
  "lists.viewSaved": { params: ["name"], en: "Saved view {name}", samples: label },
  "lists.viewUpdated": { params: ["name"], en: "Updated view {name}", samples: label },
  "lists.viewDeleted": { params: ["name"], en: "Deleted view {name}", samples: label },
  "lists.viewApplied": { params: ["name"], en: "Showing view {name}", samples: label },
  "lists.defaultSet": { params: ["name"], en: "{name} is now your default view", samples: label },
  "lists.exported": { params: ["count", "noun"], en: "Exported {count} {noun} to a CSV file.", samples: countNoun },
  "lists.exportedPartial": {
    params: ["count", "noun"],
    en: "Exported the {count} {noun} loaded so far. More exist that are not in the file; use Load more first to include them.",
    samples: countNoun,
  },
  "lists.loadMore": { params: ["noun"], en: "Load more {noun}", samples: noun },
  "lists.loading": { params: ["noun"], en: "Loading {noun}…", samples: noun },
  "lists.loaded": {
    params: ["count", "noun", "more"],
    en: "{count} {noun} loaded{more, select, true {, more available} other {}}",
    samples: [[3, "orders", true], [3, "orders", false]],
  },
  "lists.sortedLoadedOnly": { params: ["count"], en: "Sorted within the {count} rows loaded so far. Load more to include the rest.", samples: n },
  "lists.forbidden": { params: ["noun"], en: "You can't view {noun}", samples: noun },
  "lists.loadFailed": { params: ["noun"], en: "Could not load {noun}", samples: noun },
  "lists.noMatchTitle": { params: ["noun"], en: "No {noun} match these filters", samples: noun },
  "palette.noResults": { params: ["query"], en: "No pages match “{query}”.", samples: q },
  "myWork.partial": { params: ["sources"], en: "Some items could not be loaded: {sources}", samples: [["Purchasing, Sales"]] },
  "myWork.truncated": { params: ["sources"], en: "Only the newest items from {sources} are shown. Open those areas to see the rest.", samples: [["Purchasing"]] },
  "myWork.summary": {
    params: ["count", "more"],
    en: "{count, plural, =0 {Nothing is waiting for you.} other {{count}{more, select, true {+} other {}} {more, select, true {items are} other {{count, plural, one {item is} other {items are}}}} waiting for you.}}",
    samples: [[0, false], [1, false], [3, false], [5, true], [1, true]],
  },
  "format.days": { params: ["count"], en: "{count, plural, one {# day} other {# days}}", samples: [[1], [2], [30]] },
  "notes.saved": { params: ["title"], en: "{title} saved", samples: [["Call notes"]] },
};
