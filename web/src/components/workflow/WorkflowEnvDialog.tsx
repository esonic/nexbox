import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import type { WorkflowDefinition } from '@/lib/workflowApi';

const ENV_VAR_RE = /\{\{env_([A-Za-z0-9_]+)\}\}/g;
const RESERVED_ENV_NAMES = new Set(['prev_node_output']);

export function collectWorkflowEnvVars(def: WorkflowDefinition | undefined): string[] {
  if (!def?.nodes) return [];
  const names = new Set<string>();
  for (const n of def.nodes) {
    if (n.type !== 'ai') continue;
    const prompt = (n.data?.config?.prompt as string | undefined) || '';
    let m: RegExpExecArray | null;
    ENV_VAR_RE.lastIndex = 0;
    while ((m = ENV_VAR_RE.exec(prompt)) !== null) {
      const name = m[1];
      if (!RESERVED_ENV_NAMES.has(name)) names.add(name);
    }
  }
  return Array.from(names).sort();
}

export function createEmptyWorkflowEnv(names: string[]): Record<string, string> {
  const values: Record<string, string> = {};
  for (const name of names) values[name] = '';
  return values;
}

export function WorkflowEnvInputDialog({
  open,
  onOpenChange,
  names,
  values,
  onChange,
  onSubmit,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  names: string[];
  values: Record<string, string>;
  onChange: (v: Record<string, string>) => void;
  onSubmit: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Provide workflow inputs</DialogTitle>
          <DialogDescription>
            This workflow references variables via <code>{'{{env_name}}'}</code>. Leave blank for an empty value.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 py-2 max-h-[50vh] overflow-y-auto">
          {names.map((name) => (
            <div key={name} className="space-y-1">
              <label className="text-[11px] font-medium text-muted-foreground">
                {`{{env_${name}}}`}
              </label>
              <textarea
                rows={2}
                className="field-input w-full text-[12px] resize-y"
                value={values[name] ?? ''}
                onChange={(e) => onChange({ ...values, [name]: e.target.value })}
              />
            </div>
          ))}
        </div>
        <DialogFooter>
          <button
            className="px-3 py-1.5 rounded text-[12px] font-medium bg-muted text-foreground hover:bg-muted/70 transition-colors"
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </button>
          <button
            className="px-3 py-1.5 rounded text-[12px] font-medium bg-emerald-600 text-white hover:bg-emerald-700 transition-colors"
            onClick={onSubmit}
          >
            Run
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
