import { useState } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { LoginLayout } from "@/pages/login/login-layout";
import { ROUTES } from "@/lib/constants";
import { usePasswordReset } from "@/hooks/use-password-reset";
import {
  passwordResetRequestSchema,
  type PasswordResetRequestData,
} from "@/schemas/password-reset.schema";

export function ForgotPasswordPage() {
  const { t } = useTranslation("auth");
  const [submitted, setSubmitted] = useState(false);
  const { request, requesting, error } = usePasswordReset();

  const form = useForm<PasswordResetRequestData>({
    resolver: zodResolver(passwordResetRequestSchema),
    mode: "onSubmit",
    defaultValues: { email: "" },
  });

  const handleSubmit = form.handleSubmit(async (data) => {
    try {
      await request(data.email);
      setSubmitted(true);
    } catch {
      // Even on transport error we show the success message per no-enumeration policy,
      // unless the BE returned a rate-limit code we want to surface.
      if (error?.code === "rate_limit_exceeded") return;
      setSubmitted(true);
    }
  });

  return (
    <LoginLayout subtitle={t("forgotPassword.subtitle")}>
      <h2 className="text-center text-lg font-semibold">{t("forgotPassword.title")}</h2>

      {submitted ? (
        <div className="space-y-4 text-center">
          <p className="text-sm text-muted-foreground">{t("forgotPassword.success")}</p>
          <Link to={ROUTES.LOGIN} className="text-sm font-medium text-primary hover:underline">
            {t("forgotPassword.loginLink")}
          </Link>
        </div>
      ) : (
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="email">{t("forgotPassword.email")}</Label>
            <Input
              id="email"
              type="email"
              autoComplete="email"
              autoFocus
              placeholder="you@example.com"
              {...form.register("email")}
              aria-invalid={form.formState.errors.email ? true : undefined}
            />
          </div>

          {error?.code === "rate_limit_exceeded" && (
            <p className="text-sm text-destructive" role="alert">
              {t("forgotPassword.error.rateLimited")}
            </p>
          )}

          <Button type="submit" className="w-full gap-1" disabled={requesting}>
            {requesting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {requesting ? t("forgotPassword.submitting") : t("forgotPassword.submit")}
          </Button>

          <div className="text-center">
            <Link to={ROUTES.LOGIN} className="text-sm text-muted-foreground hover:underline">
              {t("forgotPassword.loginLink")}
            </Link>
          </div>
        </form>
      )}
    </LoginLayout>
  );
}
