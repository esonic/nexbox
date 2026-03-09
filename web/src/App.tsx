import { useState, useEffect } from "react";
import { BrowserRouter, Routes, Route } from "react-router-dom";
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
import { UpdateBanner } from "@/components/ui/UpdateBanner";
import LoginModal from "@/components/ui/LoginModal";
import { serverApi, authCloudApi, getToken, clearToken } from "@/lib/api";
import { useAppStore } from "@/stores/app";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 5000,
    },
  },
});

function AppLayout() {
  const [mode, setMode] = useState<string | null>(null);
  const { setCurrentUser } = useAppStore();

  useEffect(() => {
    async function init() {
      try {
        const info = await serverApi.info();
        if (info.mode === "needs_login") {
          const token = getToken();
          if (token) {
            try {
              const user = await authCloudApi.me(); // validates token
              setCurrentUser(user);
              setMode("local");        // token valid, show app
              return;
            } catch {
              clearToken();            // token invalid/expired
            }
          }
          setMode("needs_login");
          return;
        }
        setMode(info.mode);
      } catch {
        setMode("local");
      }
    }
    init();

    const handleLogout = () => { setCurrentUser(null); setMode("needs_login"); };
    window.addEventListener("boxnex:logout", handleLogout);
    return () => window.removeEventListener("boxnex:logout", handleLogout);
  }, []);

  if (mode === null) {
    return <div className="flex h-screen items-center justify-center" />;
  }

  if (mode === "needs_login") {
    return <LoginModal onSuccess={async () => {
      try {
        const user = await authCloudApi.me();
        setCurrentUser(user);
      } catch {}
      setMode("local");
    }} />;
  }

  return (
    <div className="flex h-screen overflow-hidden">
      <UpdateBanner />
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

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AppLayout />
        <Toaster position="top-center" richColors />
      </BrowserRouter>
    </QueryClientProvider>
  );
}

export default App;
