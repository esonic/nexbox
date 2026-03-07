import { useParams, useNavigate } from 'react-router-dom';
import WorkflowEditor from '@/components/workflow/WorkflowEditor';
import { useAppStore } from '@/stores/app';

export function WorkflowEditorPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const currentProject = useAppStore((state) => state.currentProject);

  if (!currentProject) {
    return (
      <div className="flex h-full items-center justify-center">
        <p className="text-muted-foreground">Please select a project first</p>
      </div>
    );
  }

  return (
    <WorkflowEditor
      workflowId={id === 'new' ? undefined : id}
      projectId={currentProject.id}
      onSave={(workflow) => {
        if (id === 'new') {
          navigate(`/workflows/${workflow.id}`);
        }
      }}
    />
  );
}
