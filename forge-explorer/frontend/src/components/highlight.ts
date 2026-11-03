import { element } from "./dom.js";

/** Small lexical highlighter for Go, JS/TS, Python, JSON and shell; other files stay plain text.
 * Tokens are text nodes. This intentionally does not claim parser/semantic accuracy.
 */
export function highlight(line: string, path: string): DocumentFragment {
  const fragment = document.createDocumentFragment();
  if (!/\.(go|[cm]?[jt]sx?|py|json|sh)$/.test(path)) {
    fragment.append(line);
    return fragment;
  }
  const pattern =
    /(\/\/.*|#.*|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|\b(?:package|import|func|function|const|let|var|return|if|else|for|type|struct|class|def|from|async|await|true|false|null|nil)\b|\b\d+(?:\.\d+)?\b)/g;
  let previous = 0;
  let tokens = 0;
  for (const match of line.matchAll(pattern)) {
    if (++tokens > 256) break;
    const index = match.index;
    fragment.append(line.slice(previous, index));
    const token = match[0];
    const kind = /^(\/\/|#)/.test(token)
      ? "comment"
      : /^["']/.test(token)
        ? "string"
        : /^\d/.test(token)
          ? "number"
          : "keyword";
    fragment.append(element("span", token, `syntax-${kind}`));
    previous = index + token.length;
  }
  fragment.append(line.slice(previous));
  return fragment;
}
