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

type AuthState = "checking" | "local" | "authed" | "unauthed";

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
    // Account-free instances enter the workspace directly. Otherwise check
    // the existing login cookie; a 401 here means the login page is needed.
    authApi.me()
      .then((status) => { if (!cancelled) setAuth(status.auth_required ? "authed" : "local"); })
      .catch(() => { if (!cancelled) setAuth("unauthed"); });
    return () => { cancelled = true; };
  }, []);

  if (auth === "checking") {
    return (
      <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
        Loading...
      </div>
    );
  }

  return (
    <Routes>
      <Route path="/login" element={auth === "local" ? <Navigate to="/" replace /> : <LoginPage onAuthed={() => setAuth("authed")} />} />
      <Route
        path="/*"
        element={
          auth === "authed" || auth === "local"
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
