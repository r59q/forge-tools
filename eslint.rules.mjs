// Shared ESLint rules for TypeScript and JavaScript projects. They mirror the
// limits in .golangci.yml: funlen, file-length-limit, nestif and lll.
// Each project's eslint.config.js applies them with its own parser.
export default {
  "max-lines-per-function": ["error", { max: 80, skipBlankLines: true, skipComments: true }],
  "max-statements": ["error", 50],
  "max-lines": ["error", { max: 400, skipBlankLines: true, skipComments: true }],
  "max-depth": ["error", 4],
  complexity: ["error", 15],
  "no-nested-ternary": "error",
  "max-len": ["error", {
    code: 140,
    ignoreUrls: true,
    ignoreStrings: true,
    ignoreTemplateLiterals: true,
    ignoreRegExpLiterals: true,
  }],
};
