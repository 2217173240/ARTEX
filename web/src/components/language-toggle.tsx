"use client";

import { type Locale, useI18n } from "@/lib/i18n";

export function LanguageToggle() {
  const { locale, setLocale, t } = useI18n();
  return (
    <select
      aria-label={t("界面语言")}
      title={t("界面语言")}
      value={locale}
      onChange={(event) => setLocale(event.target.value as Locale)}
      className="h-8 rounded-md border bg-background px-2 text-xs"
    >
      <option value="zh-CN" lang="zh-CN">
        中文
      </option>
      <option value="en" lang="en">
        English
      </option>
    </select>
  );
}
