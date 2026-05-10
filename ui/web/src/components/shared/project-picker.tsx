import { useEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Combobox, type ComboboxOption } from "@/components/ui/combobox";
import { useProjects } from "@/pages/projects/hooks/use-projects";
import type { Project } from "@/types/project";

/**
 * ProjectPicker emits one of three value shapes:
 *   - a project UUID (selected project)
 *   - `null` (the "(none)" / cleared sentinel — only when `includeNone=true`)
 *   - the literal string `"all"` (only when `includeAll=true`)
 *
 * Callers that pass both `includeAll` and `includeNone` MUST distinguish
 * `null` (none) from `"all"` in their handler. Earlier revisions collapsed
 * both sentinels to `null`, which silently lost the "all" intent.
 */
export const PROJECT_PICKER_ALL = "all" as const;
export type ProjectPickerValue = string | null;

interface ProjectPickerProps {
  value: ProjectPickerValue;
  onChange: (projectId: ProjectPickerValue) => void;
  placeholder?: string;
  className?: string;
  /** Include "(none)" option that maps to `null`. Default false. */
  includeNone?: boolean;
  /** Include "All projects" sentinel option that maps to the literal `"all"`. Default false. */
  includeAll?: boolean;
  /** Render dropdown into a portal container (useful inside dialogs). */
  portalContainer?: React.RefObject<HTMLElement | null>;
  /** Show only projects with this status. Default "active". */
  status?: "active" | "archived" | "all";
  /** Pre-loaded projects to avoid extra fetch (optional). */
  projects?: Project[];
}

const SENTINEL_NONE = "__none__";
const SENTINEL_ALL = "__all__";

export function ProjectPicker({
  value,
  onChange,
  placeholder,
  className,
  includeNone = false,
  includeAll = false,
  portalContainer,
  status = "active",
  projects: externalProjects,
}: ProjectPickerProps) {
  const { t } = useTranslation("projects");
  const { projects: hookProjects, loading, load } = useProjects();
  const loadedRef = useRef(false);

  useEffect(() => {
    if (externalProjects) return;
    if (loadedRef.current) return;
    loadedRef.current = true;
    load({ status });
  }, [externalProjects, load, status]);

  const projects = externalProjects ?? hookProjects;

  const options: ComboboxOption[] = useMemo(() => {
    const opts: ComboboxOption[] = [];
    if (includeAll) opts.push({ value: SENTINEL_ALL, label: t("picker.all") });
    if (includeNone) opts.push({ value: SENTINEL_NONE, label: t("picker.none") });
    for (const p of projects) {
      const meta = (p.metadata ?? {}) as Record<string, unknown>;
      const display = typeof meta.displayName === "string" && meta.displayName ? meta.displayName : p.slug;
      const label = display === p.slug ? p.slug : `${display} (${p.slug})`;
      opts.push({ value: p.id, label });
    }
    return opts;
  }, [projects, includeAll, includeNone, t]);

  // Map external value → combobox string. `"all"` is a sentinel emitted to the
  // caller; null falls back to the (none) entry when the picker exposes one.
  let stringValue = "";
  if (value === PROJECT_PICKER_ALL) stringValue = SENTINEL_ALL;
  else if (value === null) stringValue = includeNone ? SENTINEL_NONE : "";
  else stringValue = value;

  const handleChange = (next: string) => {
    if (next === SENTINEL_ALL) {
      onChange(PROJECT_PICKER_ALL);
      return;
    }
    if (!next || next === SENTINEL_NONE) {
      onChange(null);
      return;
    }
    onChange(next);
  };

  return (
    <Combobox
      value={stringValue}
      onChange={handleChange}
      options={options}
      placeholder={placeholder ?? (loading ? "..." : t("picker.placeholder"))}
      className={className}
      allowCustom={false}
      portalContainer={portalContainer}
    />
  );
}
