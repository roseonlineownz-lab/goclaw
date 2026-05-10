import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Info, Loader2, Save, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ConfirmDeleteDialog } from "@/components/shared/confirm-delete-dialog";
import type { Project } from "@/types/project";

interface ProjectSettingsTabProps {
  project: Project;
  onSave: (metadata: Record<string, unknown> | null) => Promise<void>;
  onDelete: () => Promise<void>;
}

export function ProjectSettingsTab({ project, onSave, onDelete }: ProjectSettingsTabProps) {
  const { t } = useTranslation("projects");
  const meta = (project.metadata ?? {}) as Record<string, unknown>;
  const [displayName, setDisplayName] = useState(typeof meta.displayName === "string" ? meta.displayName : "");
  const [description, setDescription] = useState(typeof meta.description === "string" ? meta.description : "");
  const [saving, setSaving] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  // Sync inputs when project prop changes (e.g. after refetch).
  useEffect(() => {
    const m = (project.metadata ?? {}) as Record<string, unknown>;
    setDisplayName(typeof m.displayName === "string" ? m.displayName : "");
    setDescription(typeof m.description === "string" ? m.description : "");
  }, [project]);

  const handleSave = async () => {
    setSaving(true);
    try {
      const next: Record<string, unknown> = { ...(project.metadata ?? {}) };
      const trimmedName = displayName.trim();
      const trimmedDesc = description.trim();
      if (trimmedName) next.displayName = trimmedName;
      else delete next.displayName;
      if (trimmedDesc) next.description = trimmedDesc;
      else delete next.description;
      await onSave(Object.keys(next).length === 0 ? null : next);
    } catch {
      // toast handled upstream
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <Label htmlFor="settingsSlug" className="inline-flex items-center gap-1">
          {t("columns.slug")}
          <TooltipProvider delayDuration={200}>
            <Tooltip>
              <TooltipTrigger asChild>
                <Info className="h-3.5 w-3.5 cursor-help text-muted-foreground" />
              </TooltipTrigger>
              <TooltipContent side="top">{t("detail.slugLockedTooltip")}</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </Label>
        <Input id="settingsSlug" value={project.slug} disabled className="font-mono" />
      </div>

      <div className="space-y-2">
        <Label htmlFor="settingsName">{t("settings.renameLabel")}</Label>
        <Input
          id="settingsName"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder={t("settings.renamePlaceholder")}
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="settingsDesc">{t("settings.descriptionLabel")}</Label>
        <Textarea
          id="settingsDesc"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t("settings.descriptionPlaceholder")}
          rows={4}
        />
      </div>

      <Button onClick={handleSave} disabled={saving} className="gap-2">
        {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
        {saving ? t("settings.saving") : t("settings.save")}
      </Button>

      <div className="rounded border border-destructive/40 bg-destructive/5 p-4">
        <h3 className="text-sm font-semibold text-destructive">{t("settings.dangerZone")}</h3>
        <p className="mt-1 text-xs text-muted-foreground">{t("settings.deleteHint")}</p>
        <Button
          variant="destructive"
          size="sm"
          className="mt-3 gap-1"
          onClick={() => setDeleteOpen(true)}
        >
          <Trash2 className="h-3.5 w-3.5" />
          {t("delete.confirmLabel")}
        </Button>
      </div>

      <ConfirmDeleteDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={t("delete.title")}
        description={t("delete.description", { name: displayName || project.slug })}
        confirmValue={project.slug}
        confirmLabel={t("delete.confirmLabel")}
        onConfirm={async () => {
          await onDelete();
          setDeleteOpen(false);
        }}
      />
    </div>
  );
}
