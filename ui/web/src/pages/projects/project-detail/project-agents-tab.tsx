import { useTranslation } from "react-i18next";
import { Bot } from "lucide-react";
import { EmptyState } from "@/components/shared/empty-state";

interface ProjectAgentsTabProps {
  projectId: string;
}

// Read-only view of agents attached to this project. Attach/detach lives on the
// agent detail page (see Phase 04 — agent project picker). For Phase 02 we
// simply surface the empty state until the agent-project link RPC ships.
export function ProjectAgentsTab(_props: ProjectAgentsTabProps) {
  const { t } = useTranslation("projects");
  return (
    <EmptyState
      icon={Bot}
      title={t("agents.emptyTitle")}
      description={t("agents.emptyDescription")}
    />
  );
}
