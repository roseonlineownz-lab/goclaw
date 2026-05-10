/**
 * Asserts that en/vi/zh JSON catalogs share identical key sets per namespace.
 * Drift here is a release blocker because react-i18next falls back to the raw
 * key string when a translation is missing — the user sees `forgotPassword.title`
 * verbatim instead of localized copy.
 */
import { describe, it, expect } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const LOCALES_DIR = join(__dirname, "../i18n/locales");
const LOCALES = ["en", "vi", "zh"] as const;

type JsonValue = string | number | boolean | null | JsonValue[] | { [k: string]: JsonValue };

function flattenKeys(obj: JsonValue, prefix = ""): string[] {
  if (obj === null || typeof obj !== "object" || Array.isArray(obj)) return [prefix];
  const keys: string[] = [];
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k;
    keys.push(...flattenKeys(v as JsonValue, path));
  }
  return keys;
}

function loadNamespace(locale: string, file: string): JsonValue {
  const raw = readFileSync(join(LOCALES_DIR, locale, file), "utf8");
  return JSON.parse(raw) as JsonValue;
}

const enFiles = readdirSync(join(LOCALES_DIR, "en"))
  .filter((f) => f.endsWith(".json"))
  .sort();

describe("i18n locale parity (en / vi / zh)", () => {
  it("every namespace exists in all three locales", () => {
    for (const locale of LOCALES) {
      const files = readdirSync(join(LOCALES_DIR, locale)).filter((f) => f.endsWith(".json"));
      expect(files.sort(), `${locale} is missing namespaces`).toEqual(enFiles);
    }
  });

  for (const file of enFiles) {
    it(`${file}: en/vi/zh share identical key sets`, () => {
      const enKeys = new Set(flattenKeys(loadNamespace("en", file)));
      for (const locale of ["vi", "zh"] as const) {
        const localeKeys = new Set(flattenKeys(loadNamespace(locale, file)));
        const missingInLocale = [...enKeys].filter((k) => !localeKeys.has(k));
        const extraInLocale = [...localeKeys].filter((k) => !enKeys.has(k));
        expect(missingInLocale, `${locale}/${file} missing keys`).toEqual([]);
        expect(extraInLocale, `${locale}/${file} has extra keys not in en`).toEqual([]);
      }
    });
  }
});
