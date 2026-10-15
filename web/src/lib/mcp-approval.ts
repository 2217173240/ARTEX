import type { MCPServer } from "./types";

export function parseMcpArgs(text: string): string[] {
  const value: unknown = JSON.parse(text.trim() || "[]");
  if (!Array.isArray(value) || value.some((arg) => typeof arg !== "string")) {
    throw new Error("Expected a JSON array of strings");
  }
  return value;
}

// URLs may carry credentials. Preserve the destination and parameter names only.
export function mcpApprovalDestination(server: MCPServer): string {
  if (server.transport === "stdio") return `${server.command ?? ""}\n${JSON.stringify(server.args ?? [])}`;
  try {
    const url = new URL(server.url ?? "");
    url.username = "";
    url.password = "";
    for (const key of Array.from(url.searchParams.keys())) url.searchParams.set(key, "[hidden]");
    url.hash = "";
    return url.toString();
  } catch {
    return "[invalid URL]";
  }
}
