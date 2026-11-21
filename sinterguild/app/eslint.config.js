import parser from "@typescript-eslint/parser";
import rules from "../../eslint.rules.mjs";

export default [
  { ignores: ["dist/"] },
  { files: ["**/*.ts"], languageOptions: { parser } },
  { files: ["**/*.{ts,js,mjs}"], rules },
];
