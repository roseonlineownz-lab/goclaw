import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { DetailPageSkeleton } from "@/components/shared/loading-skeleton";
import { useProjects } from "./hooks/use-projects";
import { ProjectOverviewTab } from "./project-detail/project-overview-tab";
import { ProjectMembersTab } from "./project-detail/project-members-tab";
import { ProjectAgentsTab } from "./project-detail/project-agents-tab";
import { ProjectSettingsTab } from "./project-detail/project-settings-tab";
import type { Project } from "@/types/project";

interface ProjectDetailPageProps {
  projectId: string;
  onBack: () => void;
}

export function ProjectDetailPage({ projectId, onBack }: ProjectDetailPageProps) {
  const { t } = useTranslation("projects");
  const { get, updateMetadata, deleteProject } = useProjects();
  const [project, setProject] = useState<Project | null>(null);
  const [loading, setLoading] = useState(true);

  const reload = useCallback(async () => {
    const p = await get(projectId);
    if (p) setProject(p);
  }, [projectId, get]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const p = await get(projectId);
        if (!cancelled) setProject(p);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId, get]);

  if (loading || !project) {
    return <DetailPageSkeleton tabs={4} />;
  }

  const meta = (project.metadata ?? {}) as Record<string, unknown>;
  const displayName = typeof meta.displayName === "string" && meta.displayName ? meta.displayName : project.slug;

  const handleSaveMetadata = async (metadata: Record<string, unknown> | null) => {
    await updateMetadata({ id: project.id, metadata });
    await reload();
  };

  const handleDelete = async () => {
    await deleteProject(project.id);
    onBack();
  };

  return (
    <div className="p-4 sm:p-6 pb-10">
      <div className="mb-4 flex items-center gap-3">
        <Button variant="ghost" size="sm" onClick={onBack} className="gap-1 text-muted-foreground">
          <ArrowLeft className="h-4 w-4" />
          {t("detail.back")}
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">{displayName}</h1>
        <span className="rounded border bg-muted px-2 py-0.5 font-mono text-xs">{project.slug}</span>
      </div>

      <Tabs defaultValue="overview" className="mt-6">
        <TabsList>
          <TabsTrigger value="overview">{t("detail.tabs.overview")}</TabsTrigger>
          <TabsTrigger value="members">{t("detail.tabs.members")}</TabsTrigger>
          <TabsTrigger value="agents">{t("detail.tabs.agents")}</TabsTrigger>
          <TabsTrigger value="settings">{t("detail.tabs.settings")}</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="mt-6">
          <ProjectOverviewTab project={project} />
        </TabsContent>
        <TabsContent value="members" className="mt-6">
          <ProjectMembersTab projectId={project.id} />
        </TabsContent>
        <TabsContent value="agents" className="mt-6">
          <ProjectAgentsTab projectId={project.id} />
        </TabsContent>
        <TabsContent value="settings" className="mt-6">
          <ProjectSettingsTab project={project} onSave={handleSaveMetadata} onDelete={handleDelete} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
