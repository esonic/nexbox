import type { ReactNode } from "react";

interface PageHeaderProps {
  title: string;
  subtitle: string;
  actions?: ReactNode;
}

export function PageHeader({ title, subtitle, actions }: PageHeaderProps) {
  return (
    <div className="flex items-center justify-between px-6 py-4 border-b shrink-0">
      <div className="w-full max-w-3xl mx-auto flex items-center justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold">{title}</h1>
          <p className="text-xs text-muted-foreground mt-0.5">{subtitle}</p>
        </div>
        {actions}
      </div>
    </div>
  );
}
