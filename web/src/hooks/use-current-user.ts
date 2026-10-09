"use client";

import { useEffect, useState } from "react";

import { auth, type CurrentUser } from "@/lib/auth";

const FALLBACK: CurrentUser = { id: "1", name: "ARTEX", username: "artex", email: "", avatar: "", role: "operator" };

export function useCurrentUser(): CurrentUser {
  const [user, setUser] = useState<CurrentUser>(FALLBACK);

  useEffect(() => {
    auth
      .loadSession()
      .then((u) => {
        if (u) setUser(u);
      })
      .catch(() => {
        // Keep the display fallback while the layout handles session failures.
      });
  }, []);

  return user;
}
