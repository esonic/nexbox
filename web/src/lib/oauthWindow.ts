import { toast } from "sonner";

// Reserve the browsing context before the first await. OAuth discovery and DCR
// may outlive the browser's transient activation from the Authorize click.
export async function openOAuthWindow(
  loadAuthorizationURL: () => Promise<string>,
  errorTitle = "Cannot start authorization",
): Promise<void> {
  let popup: Window | null = null;
  try {
    // Some callers discover the authorization requirement asynchronously (for
    // example, after a Lark fetch). Do not attempt an automatic popup then.
    if (window.navigator.userActivation?.isActive !== false) {
      // noopener/noreferrer in window.open would discard the handle we need to
      // navigate later. Detach the opener while still on same-origin about:blank.
      popup = window.open("about:blank", "_blank");
      if (popup) {
        popup.opener = null;
        popup.document.title = "Preparing authorization";
        popup.document.body.textContent = "Preparing authorization…";
        const policy = popup.document.createElement("meta");
        policy.name = "referrer";
        policy.content = "no-referrer";
        popup.document.head.append(policy);
      }
    }

    const authURL = new URL(await loadAuthorizationURL());
    if (authURL.protocol !== "https:" && authURL.protocol !== "http:") {
      throw new Error("The server returned an invalid authorization URL.");
    }
    if (popup && !popup.closed) {
      popup.location.replace(authURL.href);
    } else {
      // The URL is ready, so the next click opens it synchronously even if the
      // original click was blocked or its reserved tab was closed while loading.
      toast.info("Authorization link ready", {
        description: "Click Open authorization to continue.",
        duration: Infinity,
        closeButton: true,
        action: {
          label: "Open authorization",
          onClick: () => { window.open(authURL.href, "_blank", "noopener,noreferrer"); },
        },
      });
    }
  } catch (error) {
    if (popup && !popup.closed) popup.close();
    toast.error(errorTitle, {
      description: error instanceof Error ? error.message : String(error),
    });
  }
}
