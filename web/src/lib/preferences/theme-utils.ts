import type { ResolvedThemeMode, ThemeMode } from "./theme";

let themeApplication = 0;

export function resolveThemeMode(mode: ThemeMode): ResolvedThemeMode {
  if (mode === "system") {
    const prefersDark = window.matchMedia?.("(prefers-color-scheme: dark)")?.matches;
    return prefersDark ? "dark" : "light";
  }
  return mode === "dark" ? "dark" : "light";
}

export function applyThemeMode(mode: ThemeMode): ResolvedThemeMode {
  const application = ++themeApplication;
  const resolved = resolveThemeMode(mode);
  const doc = document.documentElement;
  doc.setAttribute("data-theme-mode", mode);
  doc.classList.add("disable-transitions");
  doc.classList.toggle("dark", resolved === "dark");
  doc.style.colorScheme = resolved;
  requestAnimationFrame(() => {
    // Animation-frame callbacks run before paint. Keep transitions disabled
    // through the first theme frame, then restore only the latest application.
    requestAnimationFrame(() => {
      if (application === themeApplication) doc.classList.remove("disable-transitions");
    });
  });
  return resolved;
}

export function applyThemePreset(value: string) {
  document.documentElement.setAttribute("data-theme-preset", value);
}

export function subscribeToSystemTheme(onChange: (mode: ResolvedThemeMode) => void): () => void {
  if (typeof window === "undefined") return () => undefined;
  const media = window.matchMedia?.("(prefers-color-scheme: dark)");
  if (!media) return () => undefined;

  const listener = (event: MediaQueryListEvent) => {
    onChange(event.matches ? "dark" : "light");
  };

  media.addEventListener("change", listener);

  return () => {
    media.removeEventListener("change", listener);
  };
}
