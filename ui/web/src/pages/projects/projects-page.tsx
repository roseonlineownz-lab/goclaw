import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router";
import { PageHeader } from "@/components/shared/page-header";
import { ROUTES } from "@/lib/constants";
import { ProjectDetailPage } from "./project-detail-page";
import { ProjectsListTab } from "./projects-list-tab";

export function ProjectsPage() {
  const { t } = useTranslation("projects");
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  if (id) {
    return <ProjectDetailPage projectId={id} onBack={() => navigate(ROUTES.PROJECTS)} />;
  }

  return (
    <div className="p-4 sm:p-6 pb-10">
      <PageHeader title={t("title")} description={t("description")} />
      <ProjectsListTab onSelectProject={(pid) => navigate(`/projects/${pid}`)} />
    </div>
  );
}
