import { FilePlus2 } from "lucide-react";
import { useChangeOrderStore } from "./store";

/** Registered to slot: git.menuItems */
export function ChangeOrderMenuItem({ projectId, onClose }: { projectId: string; onClose: () => void }) {
  const { loading, execute } = useChangeOrderStore();

  return (
    <button
      onClick={() => { onClose(); execute(projectId); }}
      disabled={loading}
      className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-accent transition-colors disabled:opacity-50"
    >
      <FilePlus2 className="h-3.5 w-3.5" />
      {loading ? "Creating..." : "Create Change"}
    </button>
  );
}
