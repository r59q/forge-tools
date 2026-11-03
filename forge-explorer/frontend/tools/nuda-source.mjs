import ts from "typescript";
import { createHash } from "node:crypto";

export const digest = (value) =>
  createHash("sha256").update(value).digest("hex");
export const json = (value) => JSON.stringify(value, null, 2) + "\n";

export function validateID(id) {
  if (!/^[a-z][a-z0-9-]{0,63}$/.test(id))
    throw new Error("Invalid component ID");
  return id;
}

const LANGUAGES = ["html", "css", "javascript"];
const MAX_BLOCK_LENGTH = 256_000;

export function validatePayload(id, payload) {
  validateID(id);
  if (!isPayloadFor(id, payload)) throw new Error("Malformed component payload");

  const languages = new Set();
  for (const block of payload.code) {
    if (!isCodeBlock(block) || languages.has(block.language)) {
      throw new Error("Malformed code block");
    }
    languages.add(block.language);
  }

  if (!languages.has("html") || !languages.has("css")) {
    throw new Error("HTML and CSS required");
  }
  return payload;
}

function isPayloadFor(id, payload) {
  const code = payload?.code;

  return (
    payload?.id === id &&
    payload.schemaVersion === "1.0" &&
    payload.license === "MIT" &&
    Array.isArray(code) &&
    code.length >= 1 &&
    code.length <= 3
  );
}

function isCodeBlock(block) {
  return (
    LANGUAGES.includes(block.language) &&
    typeof block.code === "string" &&
    block.code.length <= MAX_BLOCK_LENGTH &&
    block.code.trim() !== ""
  );
}

// Read the official source registry without evaluating JSX or executing upstream scripts.
export function fromRegistry(id, source) {
  validateID(id);
  const tree = ts.createSourceFile(
    "registry.tsx",
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  );
  let found;
  function property(node, key) {
    return node.properties.find(
      (p) => ts.isPropertyAssignment(p) && p.name.getText(tree) === key,
    )?.initializer;
  }
  function visit(node) {
    if (
      ts.isObjectLiteralExpression(node) &&
      property(node, "id")?.text === id
    ) {
      const code = property(node, "code");
      if (!code || !ts.isArrayLiteralExpression(code))
        throw new Error("Missing registry code");
      found = code.elements.map((block) => {
        if (!ts.isObjectLiteralExpression(block))
          throw new Error("Invalid registry block");
        const language = property(block, "language");
        const content = property(block, "code");
        if (
          !language ||
          !ts.isStringLiteral(language) ||
          !content ||
          !(
            ts.isStringLiteral(content) ||
            ts.isNoSubstitutionTemplateLiteral(content)
          )
        ) {
          throw new Error(
            "Registry code must be a literal; expressions are not executed",
          );
        }
        return {
          label: language.text.toUpperCase(),
          language: language.text,
          code: content.text,
        };
      });
    }
    ts.forEachChild(node, visit);
  }
  visit(tree);
  return validatePayload(id, {
    schemaVersion: "1.0",
    library: "NudaUI",
    id,
    license: "MIT",
    code: found,
  });
}

export function blocks(payload) {
  const extensions = { html: "html", css: "css", javascript: "js" };
  return Object.fromEntries(
    payload.code.map((block) => [
      `code.${extensions[block.language]}`,
      block.code,
    ]),
  );
}
