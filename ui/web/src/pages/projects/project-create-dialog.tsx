import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { slugify, isValidSlug } from "@/lib/slug";

interface ProjectCreateDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSubmit: (data: { slug: string; metadata: Record<string, unknown> | null }) => Promise<void>;
}

export function ProjectCreateDialog({ open, onOpenChange, onSubmit }: ProjectCreateDialogProps) {
  const { t } = useTranslation("projects");
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [description, setDescription] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!slugTouched) {
      const auto = name.trim() ? slugify(name).slice(0, 64) : "";
      setSlug(auto);
    }
  }, [name, slugTouched]);

  useEffect(() => {
    if (!open) {
      setName("");
      setSlug("");
      setSlugTouched(false);
      setDescription("");
      setSubmitting(false);
    }
  }, [open]);

  const slugInvalid = slug.length > 0 && !isValidSlug(slug);
  const slugTooShort = slug.length > 0 && slug.length < 3;
  const canSubmit = !submitting && slug.length >= 3 && !slugInvalid;

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      const metadata: Record<string, unknown> = {};
      const trimmedName = name.trim();
      const trimmedDesc = description.trim();
      if (trimmedName) metadata.displayName = trimmedName;
      if (trimmedDesc) metadata.description = trimmedDesc;
      await onSubmit({
        slug,
        metadata: Object.keys(metadata).length === 0 ? null : metadata,
      });
      onOpenChange(false);
    } catch {
      // Error toast handled upstream.
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("create.title")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="projectName">{t("create.name")}</Label>
            <Input
              id="projectName"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("create.namePlaceholder")}
              autoFocus
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="projectSlug">{t("create.slug")}</Label>
            <Input
              id="projectSlug"
              value={slug}
              onChange={(e) => {
                setSlugTouched(true);
                setSlug(e.target.value.toLowerCase());
              }}
              placeholder="acme-roadmap"
              aria-invalid={slugInvalid || undefined}
              className={slugInvalid ? "border-destructive" : undefined}
            />
            <p className="text-xs text-muted-foreground">{t("create.slugHelp")}</p>
            {slugInvalid && !slugTooShort && (
              <p className="text-xs text-destructive">{t("create.slugInvalid")}</p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="projectDesc">{t("create.description")}</Label>
            <Textarea
              id="projectDesc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t("create.descriptionPlaceholder")}
              rows={3}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
            {t("create.cancel")}
          </Button>
          <Button onClick={handleSubmit} disabled={!canSubmit} className="gap-1">
            {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {submitting ? t("create.creating") : t("create.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
