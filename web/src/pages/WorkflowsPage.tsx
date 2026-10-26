import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, Play, Edit, Trash2, Loader2 } from 'lucide-react';
import { toast } from 'sonner';
import { workflowApi, Workflow } from '@/lib/workflowApi';
import { useAppStore } from '@/stores/app';
import { WorkflowEnvInputDialog, collectWorkflowEnvVars, createEmptyWorkflowEnv } from '@/components/workflow/WorkflowEnvDialog';

export function WorkflowsPage() {
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [loading, setLoading] = useState(true);
  const [executingId, setExecutingId] = useState<string | null>(null);
  const [envDialogWorkflow, setEnvDialogWorkflow] = useState<Workflow | null>(null);
  const [envPrompts, setEnvPrompts] = useState<string[]>([]);
  const [envValues, setEnvValues] = useState<Record<string, string>>({});
  const navigate = useNavigate();
  const currentProject = useAppStore((state) => state.currentProject);

  useEffect(() => {
    if (currentProject) {
      loadWorkflows();
    }
  }, [currentProject]);

  const loadWorkflows = async () => {
    if (!currentProject) return;

    setLoading(true);
    try {
      const data = await workflowApi.list(currentProject.id);
      setWorkflows(data || []);
    } catch (error: any) {
      toast.error('Failed to load workflows: ' + error.message);
      setWorkflows([]);
    } finally {
      setLoading(false);
    }
  };

  const handleCreate = () => {
    navigate('/workflows/new');
  };

  const handleEdit = (id: string) => {
    navigate(`/workflows/${id}`);
  };

  const handleDelete = async (id: string) => {
    if (!confirm('Are you sure you want to delete this workflow?')) return;

    try {
      await workflowApi.delete(id);
      toast.success('Workflow deleted');
      loadWorkflows();
    } catch (error: any) {
      toast.error('Failed to delete: ' + error.message);
    }
  };

  const startExecution = async (workflow: Workflow, env?: Record<string, string>) => {
    setExecutingId(workflow.id);
    try {
      const execution = await workflowApi.execute(workflow.id, env ? { env } : undefined);
      toast.success(`Workflow started! Status: ${execution.status}`);
    } catch (error: any) {
      toast.error('Failed to execute: ' + error.message);
    } finally {
      setExecutingId(null);
    }
  };

  const handleExecute = async (workflow: Workflow) => {
    const names = collectWorkflowEnvVars(workflow.definition);
    if (names.length > 0) {
      setEnvDialogWorkflow(workflow);
      setEnvPrompts(names);
      setEnvValues(createEmptyWorkflowEnv(names));
      return;
    }
    await startExecution(workflow);
  };

  const handleEnvDialogRun = async () => {
    const workflow = envDialogWorkflow;
    if (!workflow) return;
    setEnvDialogWorkflow(null);
    await startExecution(workflow, envValues);
  };

  if (!currentProject) {
    return (
      <div className="flex h-full items-center justify-center">
        <p className="text-muted-foreground">Please select a project first</p>
      </div>
    );
  }

  return (
    <div className="h-full flex flex-col">
      <WorkflowEnvInputDialog
        open={!!envDialogWorkflow}
        onOpenChange={(open) => {
          if (!open) setEnvDialogWorkflow(null);
        }}
        names={envPrompts}
        values={envValues}
        onChange={setEnvValues}
        onSubmit={handleEnvDialogRun}
      />
      {/* Header */}
      <div className="border-b bg-card p-4 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Workflows</h1>
          <p className="text-sm text-muted-foreground">
            Automate tasks with visual workflow builder
          </p>
        </div>
        <button
          onClick={handleCreate}
          className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90 flex items-center gap-2"
        >
          <Plus className="w-4 h-4" />
          New Workflow
        </button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-auto p-6">
        {loading ? (
          <div className="flex items-center justify-center h-full">
            <Loader2 className="w-8 h-8 animate-spin text-muted-foreground" />
          </div>
        ) : !workflows || workflows.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-center">
            <div className="text-muted-foreground/40 mb-4">
              <svg
                className="w-24 h-24 mx-auto"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={1}
                  d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
                />
              </svg>
            </div>
            <h3 className="text-lg font-semibold mb-2">No workflows yet</h3>
            <p className="text-muted-foreground mb-4">
              Create your first workflow to automate tasks
            </p>
            <button
              onClick={handleCreate}
              className="px-6 py-3 bg-primary text-primary-foreground rounded-md hover:bg-primary/90 flex items-center gap-2"
            >
              <Plus className="w-5 h-5" />
              Create Workflow
            </button>
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {workflows.map((workflow) => (
              <div
                key={workflow.id}
                className="border rounded-lg p-4 bg-card hover:shadow-md transition-shadow"
              >
                <div className="flex items-start justify-between mb-3">
                  <div className="flex-1">
                    <h3 className="font-semibold text-lg mb-1">
                      {workflow.name}
                    </h3>
                    {workflow.description && (
                      <p className="text-sm text-muted-foreground line-clamp-2">
                        {workflow.description}
                      </p>
                    )}
                  </div>
                  <span
                    className={`px-2 py-1 text-xs rounded-full ${
                      workflow.status === 'active'
                        ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                        : workflow.status === 'archived'
                        ? 'bg-muted text-muted-foreground'
                        : 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
                    }`}
                  >
                    {workflow.status}
                  </span>
                </div>

                <div className="text-sm text-muted-foreground mb-4">
                  <div>
                    {workflow.definition?.nodes?.length || 0} nodes ·{' '}
                    {workflow.definition?.edges?.length || 0} connections
                  </div>
                  <div>Version {workflow.version}</div>
                </div>

                <div className="flex gap-2">
                  <button
                    onClick={() => handleExecute(workflow)}
                    disabled={executingId !== null}
                    className="flex-1 px-3 py-2 bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400 rounded hover:bg-green-100 dark:hover:bg-green-900/30 flex items-center justify-center gap-2 text-sm font-medium disabled:opacity-50"
                  >
                    {executingId === workflow.id ? <Loader2 className="w-4 h-4 animate-spin" /> : <Play className="w-4 h-4" />}
                    {executingId === workflow.id ? 'Starting...' : 'Run'}
                  </button>
                  <button
                    onClick={() => handleEdit(workflow.id)}
                    className="flex-1 px-3 py-2 bg-blue-50 text-blue-700 dark:bg-blue-900/20 dark:text-blue-400 rounded hover:bg-blue-100 dark:hover:bg-blue-900/30 flex items-center justify-center gap-2 text-sm font-medium"
                  >
                    <Edit className="w-4 h-4" />
                    Edit
                  </button>
                  <button
                    onClick={() => handleDelete(workflow.id)}
                    className="px-3 py-2 bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-400 rounded hover:bg-red-100 dark:hover:bg-red-900/30"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
