import parser from "@typescript-eslint/parser";
import rules from "../../eslint.rules.mjs";

export default [
  // Like the Go config, tests are exempt. NudaUI copies are references only.
  { ignores: ["dist/", "test/", "nudaui/", "src/components/nuda/"] },
  { files: ["**/*.ts"], languageOptions: { parser } },
  { files: ["**/*.{ts,js,mjs}"], rules },
];
