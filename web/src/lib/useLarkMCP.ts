import { useState, useEffect } from "react";
import { marketplaceApi, type InstalledMCP } from "@/lib/api";

export interface LarkMCPState {
  loading: boolean;
  hasLarkMCP: boolean;
  larkMCP: InstalledMCP | null;
}

function isLarkMCP(mcp: InstalledMCP): boolean {
  const name = (mcp.name + " " + mcp.mcp_key).toLowerCase();
  return name.includes("lark");
}

/**
 * Detects whether a Lark MCP is installed (globally or for the given project).
 * Uses the existing /marketplace/installed-mcps endpoint.
 */
export function useLarkMCP(projectId?: string): LarkMCPState {
  const [state, setState] = useState<LarkMCPState>({
    loading: true,
    hasLarkMCP: false,
    larkMCP: null,
  });

  useEffect(() => {
    marketplaceApi
      .listInstalledMCPs(projectId)
      .then((mcps) => {
        const found = (mcps ?? []).find(isLarkMCP) ?? null;
        setState({ loading: false, hasLarkMCP: found !== null, larkMCP: found });
      })
      .catch(() => {
        setState({ loading: false, hasLarkMCP: false, larkMCP: null });
      });
  }, [projectId]);

  return state;
}
