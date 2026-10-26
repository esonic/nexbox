// Package chrome exposes a small browser-control vocabulary over MCP, using
// chromedp for browser lifetime and CDP actions.
//
// Tool design:
//   - take_snapshot locates accessible elements; take_screenshot supplies pixels.
//     UIDs belong to one tab and the latest snapshot. Refresh after navigation,
//     tab changes or DOM replacement.
//   - click and hover use pointer events and check visibility/occlusion. fill
//     replaces editable text or selects an option by value. type_text types into
//     the focused editor; press_key supplies shortcuts and scrolling keys.
//   - navigate_page combines URL, back, forward and reload. list_pages,
//     select_page, new_page and close_page manage one shared selected tab.
//     get_page_info is a cheap URL/title check and also reports native dialogs.
//   - wait_for waits for any supplied visible text. evaluate_script calls a
//     function, awaits promises, optionally resolves UID arguments, and reports
//     JavaScript exceptions as MCP errors. Scripts should return plain data.
//   - upload_file and handle_dialog cover capabilities that page JavaScript
//     cannot reliably provide: setting local files and answering native dialogs.
//
// The resulting 17 tools cover routine browsing and forms without separate
// scroll, select-option, DOM-query or sleep tools. evaluate_script is the escape
// hatch for application-specific inspection and interactions.
//
// Operations are ordered per session to protect selected-tab/snapshot state.
// Dialog handling and dialog inspection can bypass that queue to unblock a
// pending script or navigation. A dialog must open after the tab is attached;
// dialogs owned by a different debugger must be handled there. Tools have
// bounded waits and respect request cancellation without cancelling the tab's
// long-lived CDP event loop. Browser
// launch/attachment has a separate bounded setup period. An action that times
// out may already have changed the page; inspect before repeating it.
//
// Scope: accessibility snapshots describe the current document, not an entire
// cross-origin frame tree. Pointer actions on frame-local elements fail with an
// explicit error rather than clicking incorrect top-level coordinates. Complex
// editors may need real typing instead of fill. File paths refer to the Chrome
// host (which may differ from the server for externally connected Chrome).
// Disconnecting external Chrome preserves its tabs; only close_page closes one.
// Screenshots are PNG files on the NexBox server; tools return their paths.
//
// TestMCPToolsEndToEnd verifies all 17 tools through Streamable HTTP against a
// real browser in both managed and externally connected modes. It checks page
// effects and error paths, not just successful CDP transport.
package chrome
