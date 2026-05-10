import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import type { ProjectRole } from "@/types/project";

interface ProjectRoleChipProps {
  value: ProjectRole;
  onChange?: (role: ProjectRole) => void;
  disabled?: boolean;
  size?: "sm" | "md";
}

const ROLES: ProjectRole[] = ["viewer", "member", "editor"];

export function ProjectRoleChip({ value, onChange, disabled, size = "md" }: ProjectRoleChipProps) {
  const { t } = useTranslation("projects");
  const readonly = !onChange || disabled;

  return (
    <div
      role={readonly ? "group" : "radiogroup"}
      aria-label={t("members.addRoleLabel")}
      className={cn(
        "inline-flex items-center rounded-md border p-0.5 text-xs",
        size === "sm" && "text-[11px]",
      )}
    >
      {ROLES.map((role) => {
        const active = value === role;
        const interactive = !readonly;
        return (
          <button
            key={role}
            type="button"
            role={interactive ? "radio" : undefined}
            aria-checked={interactive ? active : undefined}
            tabIndex={interactive ? (active ? 0 : -1) : -1}
            disabled={readonly}
            onClick={() => interactive && onChange!(role)}
            className={cn(
              "rounded px-2.5 py-1 font-medium capitalize transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              active
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground",
              readonly && "cursor-default",
            )}
          >
            {t(`roles.${role}`)}
          </button>
        );
      })}
    </div>
  );
}
