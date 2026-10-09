"use client";

import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useState } from "react";

import english from "./en.json";

export type Locale = "zh-CN" | "en";
export const LOCALE_STORAGE_KEY = "artex-locale";
const dictionary: Record<string, string> = english;

/** Only call with application-owned UI text. Evidence and user content stay verbatim. */
export function translate(
  locale: Locale,
  text: string,
  params?: Record<string, string | number | null | undefined>,
): string {
  const translated = locale === "en" ? (dictionary[text] ?? text) : text;
  return params
    ? translated.replace(/\{(\w+)\}/g, (match, key: string) =>
        Object.hasOwn(params, key) ? String(params[key]) : match,
      )
    : translated;
}

const I18nContext = createContext({
  locale: "zh-CN" as Locale,
  setLocale: (_locale: Locale) => {
    /* Default context retains Chinese outside the provider. */
  },
  t: (text: string, params?: Record<string, string | number | null | undefined>) => translate("zh-CN", text, params),
});

export function I18nProvider({ children }: { children: ReactNode }) {
  // The server and initial client render both use Chinese; preferences load after hydration.
  const [locale, updateLocale] = useState<Locale>("zh-CN");
  useEffect(() => {
    try {
      const stored = localStorage.getItem(LOCALE_STORAGE_KEY);
      // Accept the original fork's Zustand persistence format as well as a plain locale.
      const saved = stored?.startsWith("{") ? JSON.parse(stored).state?.locale : stored;
      if (saved === "en" || saved === "zh-CN") updateLocale(saved);
      else if (saved === "zh") updateLocale("zh-CN");
    } catch {
      // Storage can be unavailable or contain malformed data; retain the default.
    }
  }, []);
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
  const setLocale = useCallback((next: Locale) => {
    updateLocale(next);
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      /* In-memory switching still works. */
    }
  }, []);
  const value = useMemo(
    () => ({
      locale,
      setLocale,
      t: (text: string, params?: Record<string, string | number | null | undefined>) => translate(locale, text, params),
    }),
    [locale, setLocale],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  return useContext(I18nContext);
}
