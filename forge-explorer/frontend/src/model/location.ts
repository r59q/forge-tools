import type { FileContent, Location, Range, Source } from "../types.js";

const FORBIDDEN_SEGMENTS = new Set(["", ".", "..", ".git"]);

/** wholeFile returns the empty range, which denotes a whole file. */
export function wholeFile(): Range {
  return { startLine: 0, startColumn: 0, endLine: 0, endColumn: 0 };
}

/** copyLocation clones a location so callers cannot mutate shared state. */
export function copyLocation(location: Location): Location {
  return { ...location, range: { ...location.range } };
}

/** validateLocation rejects locations outside source, including from custom providers. */
export function validateLocation(source: Source, location: Location): void {
  if (location.source !== source.id || !isConfinedPath(location.path)) {
    throw new Error("Navigation must remain within its originating source.");
  }
}

/** validateRange checks a one-based, UTF-16 range against text. */
export function validateRange(text: string, range: Range): void {
  if (isWholeFile(range)) return;

  const lines = text.split("\n");
  const valid =
    Object.values(range).every(isPosition) &&
    isOrdered(range) &&
    fitsLine(lines, range.startLine, range.startColumn) &&
    fitsLine(lines, range.endLine, range.endColumn);

  if (!valid) throw new Error("The selected range is no longer valid.");
}

/** validateRead rejects a read whose identity or range does not match the request. */
export function validateRead(
  source: Source,
  file: FileContent,
  requested: Location,
): void {
  validateLocation(source, file.location);

  const { path, version } = file.location;
  const expected = requested.version;
  const versionChanged = expected !== "" && version !== expected;

  if (path !== requested.path || !version || versionChanged) {
    throw new Error("Content changed. Reload the file.");
  }
  validateRange(file.text, requested.range);
}

function isConfinedPath(path: string): boolean {
  if (!path || path.startsWith("/") || /[\\\0]/.test(path)) return false;

  return path.split("/").every((part) => !FORBIDDEN_SEGMENTS.has(part));
}

function isWholeFile(range: Range): boolean {
  return Object.values(range).every((value) => value === 0);
}

function isPosition(value: number): boolean {
  return Number.isSafeInteger(value) && value >= 1;
}

function isOrdered(range: Range): boolean {
  if (range.startLine !== range.endLine) return range.startLine < range.endLine;

  return range.startColumn <= range.endColumn;
}

// fitsLine reports whether column is at most one past the end of line.
function fitsLine(lines: string[], line: number, column: number): boolean {
  const text = lines[line - 1];

  return text !== undefined && column <= text.length + 1;
}
