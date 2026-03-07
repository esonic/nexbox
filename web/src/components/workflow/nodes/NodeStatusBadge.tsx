import { Loader2, CheckCircle2, XCircle, Clock } from 'lucide-react';

const config: Record<string, { icon: React.ReactNode; ring: string }> = {
  pending:   { icon: <Clock className="w-3 h-3 text-muted-foreground" />, ring: '' },
  running:   { icon: <Loader2 className="w-3 h-3 text-blue-500 animate-spin" />, ring: 'ring-2 ring-blue-500/60 ring-offset-1 ring-offset-background' },
  completed: { icon: <CheckCircle2 className="w-3 h-3 text-emerald-500" />, ring: 'ring-2 ring-emerald-500/60 ring-offset-1 ring-offset-background' },
  failed:    { icon: <XCircle className="w-3 h-3 text-red-500" />, ring: 'ring-2 ring-red-500/60 ring-offset-1 ring-offset-background' },
};

export function getStatusRing(status?: string): string {
  return status ? (config[status]?.ring || '') : '';
}

export function NodeStatusBadge({ status }: { status?: string }) {
  if (!status) return null;
  const c = config[status];
  if (!c) return null;

  return (
    <div className="absolute -top-1.5 -right-1.5 z-10 bg-background rounded-full p-0.5 shadow-sm border border-border/40">
      {c.icon}
    </div>
  );
}
