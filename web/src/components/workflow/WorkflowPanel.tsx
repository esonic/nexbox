import { useState, useEffect, useMemo, useRef } from 'react';
import { Plus, Trash2, Loader2, Pencil, Search, Copy, ChevronDown, ClipboardPaste } from 'lucide-react';
import { UserChip } from '@/plugins/task/components/UserPicker';
import { toast } from 'sonner';
import { workflowApi, Workflow, WorkflowDefinition } from '@/lib/workflowApi';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu';
import WorkflowEditor from './WorkflowEditor';
import WorkflowRunsView from './WorkflowRunsView';
import { formatRelativeDate } from '@/lib/utils';
import { globalWS } from '@/lib/ws';

const CLIPBOARD_WORKFLOW_TYPE = 'nexbox-workflow';
const CLIPBOARD_WORKFLOW_VERSION = 1;

interface WorkflowClipboardPayload {
  type: typeof CLIPBOARD_WORKFLOW_TYPE;
  version: number;
  name: string;
  description: string;
  definition: WorkflowDefinition;
}

interface WorkflowPanelProps {
  projectId: string;
  onEditStateChange?: (editing: boolean, name: string, onNameChange: ((name: string) => void) | null, onBack: (() => void) | null) => void;
}

type View = 'list' | 'runs' | 'design';

export function WorkflowPanel({ projectId, onEditStateChange }: WorkflowPanelProps) {
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [loading, setLoading] = useState(true);
  const [view, setView] = useState<View>('list');
  const [selectedWorkflow, setSelectedWorkflow] = useState<Workflow | null>(null);
  const [workflowName, setWorkflowName] = useState('');
  const [workflowDescription, setWorkflowDescription] = useState('');
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [isDirty, setIsDirty] = useState(false);
  const [filter, setFilter] = useState('');
  const [showFilter, setShowFilter] = useState(false);
  const filterRef = useRef<HTMLInputElement>(null);

  const filteredWorkflows = useMemo(() => {
    if (!filter.trim()) return workflows;
    const q = filter.toLowerCase();
    return workflows.filter((w) =>
      w.name.toLowerCase().includes(q) ||
      w.id.toLowerCase().includes(q) ||
      (w.created_by || '').toLowerCase().includes(q)
    );
  }, [workflows, filter]);

  useEffect(() => {
    loadWorkflows();
  }, [projectId]);

  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === 'change' && event.entity_type === 'workflow') refreshWorkflows();
    });
  }, []);

  useEffect(() => {
    if (!confirmDeleteId) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (target.closest('[data-delete-confirm]')) return;
      setConfirmDeleteId(null);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [confirmDeleteId]);

  const loadWorkflows = async () => {
    setLoading(true);
    try {
      const data = await workflowApi.list(projectId);
      setWorkflows(data || []);
    } catch (error: any) {
      toast.error('Failed to load workflows: ' + error.message);
      setWorkflows([]);
    } finally {
      setLoading(false);
    }
  };

  const refreshWorkflows = async () => {
    try {
      const data = await workflowApi.list(projectId);
      setWorkflows(data || []);
      // Keep selectedWorkflow in sync
      if (selectedWorkflow) {
        const updated = (data || []).find((w) => w.id === selectedWorkflow.id);
        if (updated) setSelectedWorkflow(updated);
      }
    } catch {
      // silent
    }
  };

  // ---- Navigation helpers ----

  const goToList = () => {
    setView('list');
    setSelectedWorkflow(null);
    setIsDirty(false);
    onEditStateChange?.(false, '', null, null);
  };

  const goToRuns = (wf: Workflow) => {
    setSelectedWorkflow(wf);
    setWorkflowName(wf.name);
    setWorkflowDescription(wf.description || '');
    setView('runs');
    onEditStateChange?.(true, wf.name, null, goToList);
  };

  const goToDesign = (wf: Workflow | null) => {
    const name = wf?.name || '';
    const desc = wf?.description || '';
    setSelectedWorkflow(wf);
    setWorkflowName(name);
    setWorkflowDescription(desc);
    setIsDirty(false);
    setView('design');
    onEditStateChange?.(true, name, (n) => setWorkflowName(n), () => {
      if (wf) {
        goToRuns(wf);
      } else {
        goToList();
      }
    });
  };

  // Keep parent in sync when name changes during design editing
  useEffect(() => {
    if (view === 'design') {
      const navigate = () => {
        if (selectedWorkflow) {
          goToRuns(selectedWorkflow);
        } else {
          goToList();
        }
      };
      const onBack = () => {
        if (isDirty) {
          toast.warning('You have unsaved changes', {
            duration: Infinity,
            action: { label: 'Leave anyway', onClick: navigate },
            cancel: { label: 'Cancel', onClick: () => {} },
          });
          return;
        }
        navigate();
      };
      onEditStateChange?.(true, workflowName, (n) => {
        setWorkflowName(n);
        setSelectedWorkflow((prev) => prev ? { ...prev, name: n } : prev);
        if (selectedWorkflow?.id) {
          const id = selectedWorkflow.id;
          setWorkflows((prev) => prev.map((w) => w.id === id ? { ...w, name: n } : w));
          workflowApi.update(id, { name: n }).catch(() => {
            toast.error('Failed to save workflow name');
          });
        }
      }, onBack);
    }
  }, [workflowName, view, isDirty, selectedWorkflow]);

  const handleCreate = () => {
    goToDesign(null);
  };

  const handleSaveWorkflow = async (savedWorkflow: Workflow) => {
    setSelectedWorkflow(savedWorkflow);
    setIsDirty(false);
    refreshWorkflows();
  };

  const handleCopyToClipboard = async (workflow: Workflow) => {
    const payload: WorkflowClipboardPayload = {
      type: CLIPBOARD_WORKFLOW_TYPE,
      version: CLIPBOARD_WORKFLOW_VERSION,
      name: workflow.name,
      description: workflow.description || '',
      definition: workflow.definition,
    };
    try {
      await navigator.clipboard.writeText(JSON.stringify(payload));
      toast.success('Workflow copied to clipboard', { duration: 1500 });
    } catch (error: any) {
      toast.error('Failed to copy: ' + error.message);
    }
  };

  const handlePasteFromClipboard = async () => {
    let text: string;
    try {
      text = await navigator.clipboard.readText();
    } catch (error: any) {
      toast.error('Failed to read clipboard: ' + error.message);
      return;
    }
    let payload: WorkflowClipboardPayload;
    try {
      const parsed = JSON.parse(text);
      if (
        !parsed ||
        parsed.type !== CLIPBOARD_WORKFLOW_TYPE ||
        typeof parsed.name !== 'string' ||
        !parsed.definition ||
        !Array.isArray(parsed.definition.nodes) ||
        !Array.isArray(parsed.definition.edges)
      ) {
        throw new Error('not a workflow');
      }
      payload = parsed;
    } catch {
      toast.error('Clipboard does not contain a valid workflow');
      return;
    }
    try {
      const created = await workflowApi.create(
        projectId,
        `${payload.name} (Copy)`,
        payload.description || '',
        payload.definition,
      );
      setWorkflows((prev) => [created, ...prev]);
      toast.success('Workflow pasted', { duration: 1500 });
    } catch (error: any) {
      toast.error('Failed to create workflow: ' + error.message);
    }
  };

  const handleDelete = async (id: string) => {
    setConfirmDeleteId(null);
    try {
      await workflowApi.delete(id);
      toast.success('Workflow deleted');
      setWorkflows((prev) => prev.filter((w) => w.id !== id));
      if (selectedWorkflow?.id === id) goToList();
    } catch (error: any) {
      toast.error('Failed to delete: ' + error.message);
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="w-6 h-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  // ---- Design view ----
  if (view === 'design') {
    return (
      <div className="flex flex-col h-full">
        <WorkflowEditor
          workflowId={selectedWorkflow?.id}
          projectId={projectId}
          workflowName={workflowName}
          workflowDescription={workflowDescription}
          onSave={handleSaveWorkflow}
          onDirtyChange={setIsDirty}
        />
      </div>
    );
  }

  // ---- Runs view ----
  if (view === 'runs' && selectedWorkflow) {
    return (
      <div className="flex flex-col h-full">
        <WorkflowRunsView
          workflow={selectedWorkflow}
          projectId={projectId}
          onEdit={() => goToDesign(selectedWorkflow)}
          onWorkflowChange={(wf) => {
            setSelectedWorkflow(wf);
            setWorkflows((prev) => prev.map((w) => w.id === wf.id ? wf : w));
          }}
        />
      </div>
    );
  }

  // ---- List view ----
  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center px-5 h-[46px] border-b border-border/20 shrink-0 gap-4">
        <div className="flex items-center gap-3 min-w-0">
          <span className="text-[13px] text-muted-foreground tabular-nums">▲ {workflows.length}</span>

          {showFilter ? (
            <div className="flex items-center gap-1.5 bg-accent/30 rounded-md px-2.5 py-1 border border-border/20">
              <Search className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
              <input
                ref={filterRef}
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Escape') { setShowFilter(false); setFilter(''); } }}
                placeholder="Filter workflows…"
                className="bg-transparent outline-none text-xs w-40 placeholder:text-muted-foreground/60"
              />
            </div>
          ) : (
            <button
              onClick={() => { setShowFilter(true); setTimeout(() => filterRef.current?.focus(), 50); }}
              className="flex items-center gap-1.5 px-2 py-1 rounded-md text-xs text-muted-foreground hover:text-foreground border border-border/30 hover:border-border/50 transition-colors"
            >
              <Search className="h-3 w-3" />
              <span>Filter</span>
            </button>
          )}
        </div>

        <div className="ml-auto">
          <div className="inline-flex items-stretch rounded-md border border-border/30 hover:border-border/50 transition-colors overflow-hidden">
            <button
              onClick={handleCreate}
              className="flex items-center gap-1 px-2 py-1 text-xs text-muted-foreground hover:text-foreground hover:bg-accent/40 transition-colors"
              title="New workflow"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>New</span>
            </button>
            <div className="w-px bg-border/30" />
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  className="flex items-center px-1 text-muted-foreground hover:text-foreground hover:bg-accent/40 transition-colors"
                  title="More create options"
                  aria-label="More create options"
                >
                  <ChevronDown className="h-3.5 w-3.5" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={handlePasteFromClipboard}>
                  <ClipboardPaste className="h-3.5 w-3.5" />
                  <span>From clipboard</span>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        {workflows.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-center px-4">
            <div className="text-muted-foreground/40 mb-3">
              <svg className="w-16 h-16 mx-auto" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1}
                  d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
              </svg>
            </div>
            <h3 className="font-medium mb-1">No workflows yet</h3>
            <p className="text-sm text-muted-foreground mb-4">Create your first workflow</p>
            <Button onClick={handleCreate} size="sm" className="gap-2">
              <Plus className="w-4 h-4" />
              Create Workflow
            </Button>
          </div>
        ) : filteredWorkflows.length === 0 ? (
          <div className="flex items-center justify-center h-24 text-[12px] text-muted-foreground/50">
            No results for "{filter}"
          </div>
        ) : (
          <div className="space-y-px">
            {filteredWorkflows.map((workflow) => (
              <div
                key={workflow.id}
                role="button"
                tabIndex={0}
                className="group/wf flex items-center h-[42px] border-b border-border/10 hover:bg-[#ffffff06] dark:hover:bg-[#ffffff06] transition-colors cursor-pointer select-none"
                onClick={() => goToRuns(workflow)}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') goToRuns(workflow); }}
              >
                {/* Short ID */}
                <span
                  className="w-[64px] shrink-0 pl-3 text-[11px] text-muted-foreground font-mono tabular-nums truncate hover:text-foreground cursor-copy"
                  title={workflow.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    navigator.clipboard.writeText(workflow.id);
                    toast.success("Copied", { duration: 1500 });
                  }}
                >
                  {workflow.id.replace(/-/g, '').slice(0, 6).toUpperCase()}
                </span>

                {/* Name + meta */}
                <div className="flex-1 min-w-0 flex items-center gap-2 pr-2">
                  <span className="truncate text-[13px] text-foreground font-medium">
                    {workflow.name}
                  </span>
                  <span className="shrink-0 text-[10px] text-muted-foreground/50 tabular-nums">
                    {workflow.definition?.nodes?.length || 0} nodes · v{workflow.version}
                  </span>
                  {workflow.schedule && (() => {
                    try {
                      const s = JSON.parse(workflow.schedule);
                      if (s.type === 'interval') return <span className="shrink-0 text-[10px] text-muted-foreground/60">⏱ {s.interval_hours}h</span>;
                      if (s.type === 'weekly') {
                        const days = (s.days || []).map((d: number) => ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'][d]).join(' ');
                        return <span className="shrink-0 text-[10px] text-muted-foreground/60">⏱ {days} {String(s.hour ?? 9).padStart(2, '0')}:{String(s.minute ?? 0).padStart(2, '0')}</span>;
                      }
                    } catch {}
                    return null;
                  })()}
                  {workflow.is_running && (
                    <Loader2 className="w-3 h-3 text-blue-500 animate-spin shrink-0" />
                  )}
                </div>

                {/* Right side */}
                <div className="flex items-center shrink-0">
                  {/* Hover actions */}
                  <div className="hidden group-hover/wf:flex items-center gap-0.5 mr-1">
                    {/* Edit (Design) button */}
                    <button
                      onClick={(e) => { e.stopPropagation(); goToDesign(workflow); }}
                      className="p-1 rounded text-muted-foreground/60 hover:text-foreground hover:bg-accent/60 transition-colors"
                      title="Edit design"
                    >
                      <Pencil className="w-3 h-3" />
                    </button>
                    {/* Copy to clipboard button */}
                    <button
                      onClick={(e) => { e.stopPropagation(); handleCopyToClipboard(workflow); }}
                      className="p-1 rounded text-muted-foreground/60 hover:text-foreground hover:bg-accent/60 transition-colors"
                      title="Copy workflow to clipboard"
                    >
                      <Copy className="w-3 h-3" />
                    </button>
                    {/* Delete button */}
                    <div className="relative">
                      <button
                        onClick={(e) => { e.stopPropagation(); setConfirmDeleteId(workflow.id); }}
                        className="p-1 rounded text-muted-foreground/60 hover:text-destructive hover:bg-destructive/10 transition-colors"
                        title="Delete"
                      >
                        <Trash2 className="w-3 h-3" />
                      </button>
                      {confirmDeleteId === workflow.id && (
                        <div data-delete-confirm className="absolute right-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md p-1.5 flex gap-1 whitespace-nowrap">
                          <button
                            onClick={(e) => { e.stopPropagation(); handleDelete(workflow.id); }}
                            className="px-2 py-0.5 text-xs bg-destructive text-destructive-foreground rounded hover:bg-destructive/90"
                          >
                            Delete
                          </button>
                          <button
                            onClick={(e) => { e.stopPropagation(); setConfirmDeleteId(null); }}
                            className="px-2 py-0.5 text-xs bg-muted text-muted-foreground rounded hover:bg-accent"
                          >
                            Cancel
                          </button>
                        </div>
                      )}
                    </div>
                  </div>

                  {/* Creator */}
                  {workflow.created_by && workflow.created_by !== 'local' && (
                    <UserChip userName={workflow.created_by} className="text-blue-400" />
                  )}

                  {/* Date */}
                  <span
                    className="shrink-0 text-[11px] text-muted-foreground pr-2 ml-1 tabular-nums text-right"
                    title={new Date(workflow.updated_at).toLocaleString()}
                  >
                    {formatRelativeDate(workflow.updated_at)}
                  </span>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
