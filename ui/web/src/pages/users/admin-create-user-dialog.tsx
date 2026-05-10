import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { toast } from "@/stores/use-toast-store";
import { userFriendlyError } from "@/lib/error-utils";
import { useAuthStore } from "@/stores/use-auth-store";
import {
  adminCreateUserSchema,
  type AdminCreateUserData,
  type AdminCreateUserRole,
} from "@/schemas/admin-create-user.schema";

interface AdminCreateUserDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSubmit: (data: AdminCreateUserData) => Promise<void>;
}

export function AdminCreateUserDialog({ open, onOpenChange, onSubmit }: AdminCreateUserDialogProps) {
  const { t } = useTranslation("auth");
  // Only the owner (BE root) may create peer admins per users_handlers.go:99-105.
  // Plain admins still create member/viewer; the BE returns 403 if they try admin.
  const callerRole = useAuthStore((s) => s.role);
  const canCreateAdmin = callerRole === "owner";

  const form = useForm<AdminCreateUserData>({
    resolver: zodResolver(adminCreateUserSchema),
    mode: "onSubmit",
    defaultValues: { email: "", display_name: "", password: "", role: "member" },
  });

  useEffect(() => {
    if (!open) form.reset({ email: "", display_name: "", password: "", role: "member" });
  }, [open, form]);

  const role = form.watch("role");
  const submitting = form.formState.isSubmitting;

  const handleSubmit = form.handleSubmit(async (data) => {
    try {
      await onSubmit(data);
      toast.success(t("adminUsers.create.successToast"), t("adminUsers.create.shareWarning"));
      onOpenChange(false);
    } catch (err) {
      toast.error(t("adminUsers.create.error.generic"), userFriendlyError(err));
    }
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("adminUsers.create.title")}</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="newUserEmail">{t("adminUsers.create.email")}</Label>
            <Input
              id="newUserEmail"
              type="email"
              autoComplete="off"
              autoFocus
              placeholder="user@example.com"
              {...form.register("email")}
              aria-invalid={form.formState.errors.email ? true : undefined}
            />
            {form.formState.errors.email && (
              <p className="text-xs text-destructive">{t("adminUsers.create.error.invalidEmail")}</p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="newUserDisplay">{t("adminUsers.create.displayName")}</Label>
            <Input
              id="newUserDisplay"
              autoComplete="off"
              {...form.register("display_name")}
              aria-invalid={form.formState.errors.display_name ? true : undefined}
            />
            {form.formState.errors.display_name && (
              <p className="text-xs text-destructive">{t("adminUsers.create.error.displayNameTooShort")}</p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="newUserPassword">{t("adminUsers.create.password")}</Label>
            <Input
              id="newUserPassword"
              type="password"
              autoComplete="new-password"
              {...form.register("password")}
              aria-invalid={form.formState.errors.password ? true : undefined}
            />
            <p className="text-xs text-muted-foreground">{t("adminUsers.create.passwordHint")}</p>
            {form.formState.errors.password && (
              <p className="text-xs text-destructive">{t("adminUsers.create.error.weakPassword")}</p>
            )}
          </div>

          <div className="space-y-2">
            <Label>{t("adminUsers.create.role")}</Label>
            <Select
              value={role}
              onValueChange={(v) => form.setValue("role", v as AdminCreateUserRole, { shouldValidate: true })}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {canCreateAdmin && (
                  <SelectItem value="admin">{t("adminUsers.roles.admin")}</SelectItem>
                )}
                <SelectItem value="member">{t("adminUsers.roles.member")}</SelectItem>
                <SelectItem value="viewer">{t("adminUsers.roles.viewer")}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <p className="rounded border border-amber-400/40 bg-amber-50 p-3 text-xs text-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
            {t("adminUsers.create.shareWarning")}
          </p>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
              {t("adminUsers.create.cancel")}
            </Button>
            <Button type="submit" disabled={submitting} className="gap-1">
              {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              {submitting ? t("adminUsers.create.submitting") : t("adminUsers.create.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
