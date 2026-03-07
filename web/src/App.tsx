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
import { PublishPage } from "@/pages/marketplace/PublishPage";
import { PackageDetailPage } from "@/pages/marketplace/PackageDetailPage";
import { PublishVersionPage } from "@/pages/marketplace/PublishVersionPage";
import { UpdateBanner } from "@/components/ui/UpdateBanner";
import { LoginModal } from "@/components/ui/LoginModal";
import { serverApi } from "@/lib/api";

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

  useEffect(() => {
    serverApi.info().then((info) => setMode(info.mode)).catch(() => setMode("local"));
  }, []);

  if (mode === null) {
    return <div className="flex h-screen items-center justify-center" />;
  }

  if (mode === "needs_login") {
    return <LoginModal />;
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
          <Route path="/marketplace/publish" element={<PublishPage />} />
          <Route path="/marketplace/publish/:packageId" element={<PublishVersionPage />} />
          <Route path="/marketplace/:packageId" element={<PackageDetailPage />} />
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
