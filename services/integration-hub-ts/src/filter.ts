// CloudEvents type patterns for subscriptions (FR-35.9, event_types in §6.35.4).
// A pattern and an event type are both dot-separated. Segments are lowercase
// alphanumerics and match literally, except:
//   `*`   matches exactly one segment;
//   `**`  as the last segment matches one or more trailing segments.
// `**` may not be the first segment, so a subscription cannot match every event
// by accident. Pure: the subscription service validates on create and the
// dispatcher matches on publish.

const SEGMENT = /^[a-z0-9]+$/;

/** Returns null when the pattern is valid, otherwise a message for the caller. */
export function validatePattern(pattern: string): string | null {
  if (pattern.length === 0) {
    return "pattern is empty";
  }
  const segments = pattern.split(".");
  for (let i = 0; i < segments.length; i += 1) {
    const seg = segments[i];
    if (seg === "**") {
      if (i === 0) {
        return "'**' cannot be the first segment";
      }
      if (i !== segments.length - 1) {
        return "'**' is only allowed as the last segment";
      }
      continue;
    }
    if (seg === "*") {
      continue;
    }
    if (!SEGMENT.test(seg)) {
      return `segment '${seg}' must be lowercase letters or digits`;
    }
  }
  return null;
}

/** True when the event type matches the pattern. An invalid pattern never matches. */
export function matchesType(pattern: string, type: string): boolean {
  if (validatePattern(pattern) !== null) {
    return false;
  }
  const ps = pattern.split(".");
  const ts = type.split(".");
  for (let i = 0; i < ps.length; i += 1) {
    if (ps[i] === "**" && i === ps.length - 1) {
      return ts.length > i;
    }
    if (i >= ts.length) {
      return false;
    }
    if (ps[i] !== "*" && ps[i] !== ts[i]) {
      return false;
    }
  }
  return ps.length === ts.length;
}

/** True when any pattern matches. An empty pattern list matches nothing. */
export function matchesAny(patterns: readonly string[], type: string): boolean {
  return patterns.some((p) => matchesType(p, type));
}
