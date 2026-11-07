import type { Project } from "@/types";

const nameCollator = new Intl.Collator(undefined, { sensitivity: "base", numeric: true });

/** Project names lead; namespace and ID make duplicate names deterministic. */
export function compareProjectsByName(a: Project, b: Project): number {
  return nameCollator.compare(a.name, b.name)
    || nameCollator.compare(a.namespace_id || "", b.namespace_id || "")
    || a.id.localeCompare(b.id);
}

export function sortProjectsByName(projects: Project[]): Project[] {
  return [...projects].sort(compareProjectsByName);
}
