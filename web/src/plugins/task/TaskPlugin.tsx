import { useState, useEffect } from "react";
import { useSearchParams } from "react-router-dom";
import { TasksPage } from "./TasksPanel";
import { TaskDetail } from "./TaskDetail";

export function TaskPlugin({ projectId }: { projectId: string }) {
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [searchParams, setSearchParams] = useSearchParams();

  useEffect(() => {
    const openTask = searchParams.get("open_task");
    if (openTask) {
      setSelectedTaskId(openTask);
      setSearchParams((prev) => {
        prev.delete("open_task");
        prev.delete("comment_id");
        return prev;
      }, { replace: true });
    }
  }, []);

  return (
    <>
      <div className={selectedTaskId ? "hidden" : "contents"}>
        <TasksPage
          embedded
          onSelectTask={(id) => setSelectedTaskId(id)}
          currentProjectId={projectId}
          refreshKey={refreshKey}
        />
      </div>
      {selectedTaskId && (
        <TaskDetail
          taskId={selectedTaskId}
          onBack={() => { setRefreshKey((k) => k + 1); setSelectedTaskId(null); }}
        />
      )}
    </>
  );
}
