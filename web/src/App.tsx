import { useState, useEffect } from "react";
import { BrowserRouter, Routes, Route, Navigate, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { AppSidebar } from "@/components/layout/AppSidebar";
import { SessionWorkspace } from "@/pages/SessionWorkspace";
import { ConnectPage } from "@/pages/ConnectPage";
import { SettingsPage } from "@/pages/SettingsPage";
import { WorkflowsPage } from "@/pages/WorkflowsPage";
import { WorkflowEditorPage } from "@/pages/WorkflowEditorPage";
import { ProjectsPage } from "@/pages/ProjectsPage";
import { MarketplacePage } from "@/pages/MarketplacePage";
import { LoginPage } from "@/pages/LoginPage";
import { authApi } from "@/lib/api";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 5000,
    },
  },
});

type AuthState = "checking" | "authed" | "unauthed";

function MainLayout() {
  return (
    <div className="flex h-full overflow-hidden">
      <AppSidebar />
      <main className="flex-1 overflow-hidden">
        <Routes>
          <Route path="/" element={<SessionWorkspace />} />
          <Route path="/projects" element={<ProjectsPage />} />
          <Route path="/connect" element={<ConnectPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="/workflows" element={<WorkflowsPage />} />
          <Route path="/workflows/:id" element={<WorkflowEditorPage />} />
          <Route path="/marketplace" element={<MarketplacePage />} />
        </Routes>
      </main>
    </div>
  );
}

function AppRoutes() {
  const [auth, setAuth] = useState<AuthState>("checking");
  const location = useLocation();

  useEffect(() => {
    let cancelled = false;
    // On every mount (and on route change), verify the JWT cookie still
    // resolves to a user. The /api/auth/me endpoint is whitelisted from
    // the redirect interceptor — a 401 here just means "not logged in",
    // not a session that expired mid-action.
    authApi.me()
      .then(() => { if (!cancelled) setAuth("authed"); })
      .catch(() => { if (!cancelled) setAuth("unauthed"); });
    return () => { cancelled = true; };
    // Re-check whenever we land on a non-login route (covers post-login
    // navigation that swaps the cookie without a full reload).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (auth === "checking") {
    return (
      <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
        Loading...
      </div>
    );
  }

  // Always render /login regardless of auth state — it manages its own state.
  return (
    <Routes>
      <Route path="/login" element={<LoginPage onAuthed={() => setAuth("authed")} />} />
      <Route
        path="/*"
        element={
          auth === "authed"
            ? <MainLayout />
            : <Navigate to={`/login?from=${encodeURIComponent(location.pathname + location.search)}`} replace />
        }
      />
    </Routes>
  );
}

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AppRoutes />
        <Toaster position="top-center" richColors />
      </BrowserRouter>
    </QueryClientProvider>
  );
}

export default App;
