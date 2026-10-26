import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { promptTemplatesApi, type PromptTemplate } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

const NEW = -1;

export function PromptTemplatesSettings() {
  const [templates, setTemplates] = useState<PromptTemplate[]>([]);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [content, setContent] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    promptTemplatesApi.list().then(setTemplates).catch(() => { /* non-fatal */ });
  }, []);

  const startEdit = (index = NEW) => {
    const t = templates[index];
    setEditingId(index);
    setName(t?.name ?? "");
    setContent(t?.content ?? "");
  };

  const persist = async (next: PromptTemplate[]) => {
    setSaving(true);
    try {
      setTemplates(await promptTemplatesApi.replace(next));
      return true;
    } catch (e) {
      toast.error("Failed to save prompt templates", { description: String(e) });
      return false;
    } finally {
      setSaving(false);
    }
  };

  const handleSave = async () => {
    const trimmed = name.trim();
    if (!trimmed || !content.trim()) {
      toast.error("Name and content are required");
      return;
    }
    if (templates.some((t, i) => i !== editingId && t.name.toLowerCase() === trimmed.toLowerCase())) {
      toast.error("A template with this name already exists");
      return;
    }
    const next = editingId === NEW
      ? [...templates, { name: trimmed, content }]
      : templates.map((t, i) => (i === editingId ? { name: trimmed, content } : t));
    if (await persist(next)) setEditingId(null);
  };

  const handleDelete = (index: number) => {
    void persist(templates.filter((_, i) => i !== index));
  };

  return (
    <section className="mt-8 space-y-3">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium">Prompt Templates</h3>
          <p className="text-xs text-muted-foreground mt-0.5">
            Saved prompts you can pick from the chat input by typing "/".
          </p>
        </div>
        {editingId === null && (
          <Button size="sm" variant="outline" onClick={() => startEdit()}>
            <Plus className="h-4 w-4 mr-1" />
            Add
          </Button>
        )}
      </div>

      {editingId !== null && (
        <div className="rounded-lg border bg-card p-4 space-y-3">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Name"
            maxLength={100}
            autoFocus
          />
          <Textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            placeholder="Prompt text"
            className="min-h-[140px] font-mono"
          />
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setEditingId(null)} disabled={saving}>
              Cancel
            </Button>
            <Button size="sm" onClick={handleSave} disabled={saving}>
              Save
            </Button>
          </div>
        </div>
      )}

      <div className="rounded-lg border bg-card divide-y">
        {templates.length === 0 && (
          <div className="px-4 py-3 text-xs text-muted-foreground">No templates yet.</div>
        )}
        {templates.map((t, i) => (
          <div key={t.name} className="flex items-center gap-3 px-4 py-3">
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium truncate">{t.name}</div>
              <div className="text-xs text-muted-foreground truncate">{t.content.replace(/\s+/g, " ")}</div>
            </div>
            <Button size="icon" variant="ghost" className="h-8 w-8" onClick={() => startEdit(i)} disabled={saving} aria-label={`Edit ${t.name}`}>
              <Pencil className="h-4 w-4" />
            </Button>
            <Button size="icon" variant="ghost" className="h-8 w-8" onClick={() => handleDelete(i)} disabled={saving} aria-label={`Delete ${t.name}`}>
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
        ))}
      </div>
    </section>
  );
}
