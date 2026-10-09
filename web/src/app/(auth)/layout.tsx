import type { ReactNode } from "react";

import { LanguageToggle } from "@/components/language-toggle";

export default function AuthLayout({ children }: { readonly children: ReactNode }) {
  return (
    <>
      <div className="fixed top-4 right-4 z-50">
        <LanguageToggle />
      </div>
      {children}
    </>
  );
}
