import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, X } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { UserPickerCombobox } from "@/components/shared/user-picker-combobox";
import { useContactResolver } from "@/hooks/use-contact-resolver";
import { formatUserLabel } from "@/lib/format-user-label";
import { ProjectRoleChip } from "./project-role-chip";
import type { ProjectRole } from "@/types/project";

interface ProjectBulkGrantDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Already-granted user UUIDs to exclude from suggestions. */
  excludeUserIds?: string[];
  onSubmit: (userIds: string[], role: ProjectRole) => Promise<void>;
}

export function ProjectBulkGrantDialog({ open, onOpenChange, excludeUserIds, onSubmit }: ProjectBulkGrantDialogProps) {
  const { t } = useTranslation("projects");
  const portalRef = useRef<HTMLDivElement>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [role, setRole] = useState<ProjectRole>("member");
  const [submitting, setSubmitting] = useState(false);
  const { resolve } = useContactResolver(selected);

  const reset = () => {
    setSelected([]);
    setSearch("");
    setRole("member");
  };

  const addUser = (uuid: string) => {
    const trimmed = uuid.trim();
    if (!trimmed) return;
    if (excludeUserIds?.includes(trimmed)) return;
    if (selected.includes(trimmed)) return;
    setSelected((prev) => [...prev, trimmed]);
    setSearch("");
  };

  const removeUser = (uuid: string) => {
    setSelected((prev) => prev.filter((u) => u !== uuid));
  };

  const handleSubmit = async () => {
    if (selected.length === 0) return;
    setSubmitting(true);
    try {
      await onSubmit(selected, role);
      reset();
      onOpenChange(false);
    } catch {
      // Toast handled upstream.
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) reset();
        onOpenChange(next);
      }}
    >
      <DialogContent className="max-h-[90vh] flex flex-col">
        <div ref={portalRef} />
        <DialogHeader>
          <DialogTitle>{t("members.addTitle")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-4 overflow-y-auto min-h-0">
          <div className="space-y-2">
            <Label>{t("members.addUsersLabel")}</Label>
            <UserPickerCombobox
              value={search}
              onChange={setSearch}
              onSelect={addUser}
              placeholder={t("members.addUsersLabel")}
              valueMode="uuid"
              allowCustom={false}
              portalContainer={portalRef}
            />
            {selected.length > 0 && (
              <div className="flex flex-wrap gap-1.5 pt-1">
                {selected.map((uuid) => (
                  <Badge key={uuid} variant="secondary" className="gap-1 pr-1">
                    {formatUserLabel(uuid, resolve)}
                    <button
                      type="button"
                      onClick={() => removeUser(uuid)}
                      className="ml-0.5 rounded-full p-0.5 hover:bg-muted"
                      aria-label="remove"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </Badge>
                ))}
              </div>
            )}
          </div>

          <div className="space-y-2">
            <Label>{t("members.addRoleLabel")}</Label>
            <ProjectRoleChip value={role} onChange={setRole} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
            {t("create.cancel")}
          </Button>
          <Button onClick={handleSubmit} disabled={selected.length === 0 || submitting} className="gap-1">
            {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {submitting ? t("members.adding") : t("members.addSubmit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
