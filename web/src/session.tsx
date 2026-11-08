import { createContext, useContext } from "react";
import type { Me } from "./api";

interface SessionValue {
  me: Me;
  /** What people sign in with, as the install names it ("Gitea", "GitLab"). */
  providerName: string;
  /** Reads /me again, e.g. after creating an organization. */
  reload: () => Promise<void>;
}

export const SessionContext = createContext<SessionValue | null>(null);

/** Who is signed in: a user (session cookie) or the admin token. */
export function useSession(): SessionValue {
  const v = useContext(SessionContext);
  if (!v) throw new Error("useSession outside the signed-in app");
  return v;
}
