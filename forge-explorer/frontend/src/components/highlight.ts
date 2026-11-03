import { h } from "./dom.js";

const HIGHLIGHTED_FILES = /\.(go|[cm]?[jt]sx?|py|json|sh)$/;
const MAX_TOKENS = 256;

const KEYWORDS = [
  "package", "import", "func", "function", "const", "let", "var", "return",
  "if", "else", "for", "type", "struct", "class", "def", "from", "async",
  "await", "true", "false", "null", "nil",
];

const TOKEN = new RegExp(
  [
    String.raw`\/\/.*|#.*`, // comments
    String.raw`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`, // strings
    String.raw`\b(?:${KEYWORDS.join("|")})\b`,
    String.raw`\b\d+(?:\.\d+)?\b`, // numbers
  ].join("|"),
  "g",
);

/** highlight is a small lexical highlighter for Go, JS/TS, Python, JSON and shell.
 * Other files stay plain text. Tokens are text nodes; this is not a parser.
 */
export function highlight(line: string, path: string): DocumentFragment {
  const fragment = document.createDocumentFragment();
  if (!HIGHLIGHTED_FILES.test(path)) {
    fragment.append(line);
    return fragment;
  }

  let previous = 0;
  let tokens = 0;
  for (const match of line.matchAll(TOKEN)) {
    if (++tokens > MAX_TOKENS) break;

    const token = match[0];
    fragment.append(
      line.slice(previous, match.index),
      h("span", { class: `syntax-${tokenKind(token)}` }, token),
    );
    previous = match.index + token.length;
  }

  fragment.append(line.slice(previous));
  return fragment;
}

function tokenKind(token: string): string {
  if (token.startsWith("//") || token.startsWith("#")) return "comment";
  if (token.startsWith('"') || token.startsWith("'")) return "string";
  if (/^\d/.test(token)) return "number";

  return "keyword";
}
