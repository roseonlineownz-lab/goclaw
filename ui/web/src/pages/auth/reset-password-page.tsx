import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
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
  passwordResetConfirmSchema,
  type PasswordResetConfirmData,
} from "@/schemas/password-reset.schema";

const META_REFERRER_ID = "reset-password-no-referrer";

// Strip the ?token=... query string so the value cannot leak via window.location
// once the page has resolved — token-in-URL is otherwise sent in the Referer
// header to any 3rd-party script the page loads (analytics, fonts, error tracking).
function stripTokenFromUrl() {
  const url = new URL(window.location.href);
  url.search = "";
  window.history.replaceState({}, "", url.pathname);
}

// Inject `<meta name="referrer" content="no-referrer">` while the reset page
// is mounted to keep the token out of any sub-resource Referer header.
function useNoReferrerMeta() {
  useEffect(() => {
    let meta = document.head.querySelector<HTMLMetaElement>(`meta#${META_REFERRER_ID}`);
    let created = false;
    if (!meta) {
      meta = document.createElement("meta");
      meta.id = META_REFERRER_ID;
      meta.name = "referrer";
      meta.content = "no-referrer";
      document.head.appendChild(meta);
      created = true;
    }
    return () => {
      if (created && meta?.parentNode) meta.parentNode.removeChild(meta);
    };
  }, []);
}

export function ResetPasswordPage() {
  const { t } = useTranslation("auth");
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [done, setDone] = useState(false);
  const { confirm, confirming, error } = usePasswordReset();

  // Capture the token before stripping it from the URL.
  const [token] = useState<string>(() => searchParams.get("token") ?? "");

  useNoReferrerMeta();

  const form = useForm<PasswordResetConfirmData>({
    resolver: zodResolver(passwordResetConfirmSchema),
    mode: "onSubmit",
    defaultValues: { token, password: "", confirm: "" },
  });

  const handleSubmit = form.handleSubmit(async (data) => {
    try {
      await confirm(data.token, data.password);
      setDone(true);
      stripTokenFromUrl();
      setTimeout(() => navigate(ROUTES.LOGIN, { replace: true }), 1500);
    } catch {
      // Even on failure, the token is consumed best-effort — clear it so a retry
      // doesn't keep leaking it.
      stripTokenFromUrl();
    }
  });

  if (!token) {
    return (
      <LoginLayout subtitle={t("resetPassword.subtitle")}>
        <h2 className="text-center text-lg font-semibold">{t("resetPassword.title")}</h2>
        <p className="text-center text-sm text-destructive" role="alert">
          {t("resetPassword.tokenMissing")}
        </p>
        <div className="text-center">
          <Link to={ROUTES.LOGIN} className="text-sm text-muted-foreground hover:underline">
            {t("resetPassword.loginLink")}
          </Link>
        </div>
      </LoginLayout>
    );
  }

  return (
    <LoginLayout subtitle={t("resetPassword.subtitle")}>
      <h2 className="text-center text-lg font-semibold">{t("resetPassword.title")}</h2>

      {done ? (
        <p className="text-center text-sm text-muted-foreground">{t("resetPassword.success")}</p>
      ) : (
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="newPassword">{t("resetPassword.password")}</Label>
            <Input
              id="newPassword"
              type="password"
              autoComplete="new-password"
              autoFocus
              {...form.register("password")}
              aria-invalid={form.formState.errors.password ? true : undefined}
            />
            <p className="text-xs text-muted-foreground">{t("bootstrap.passwordHint")}</p>
            {form.formState.errors.password && (
              <p className="text-xs text-destructive">{t("resetPassword.error.weakPassword")}</p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="confirmPassword">{t("resetPassword.confirm")}</Label>
            <Input
              id="confirmPassword"
              type="password"
              autoComplete="new-password"
              {...form.register("confirm")}
              aria-invalid={form.formState.errors.confirm ? true : undefined}
            />
            {form.formState.errors.confirm && (
              <p className="text-xs text-destructive">{t("resetPassword.error.passwordsDontMatch")}</p>
            )}
          </div>

          {error && (
            <p className="text-sm text-destructive" role="alert">
              {(() => {
                // BE returns flat shape `{ "error": "<code>", "message": "..." }`
                // for password-reset failures. http-client surfaces the BE error
                // string as `apiError.message` when no nested `{code}` exists.
                const m = (error.message ?? "").toLowerCase();
                if (m === "weak_password") return t("resetPassword.error.weakPassword");
                if (m === "invalid_token") return t("resetPassword.error.tokenExpired");
                return t("resetPassword.error.generic");
              })()}
            </p>
          )}

          <Button type="submit" className="w-full gap-1" disabled={confirming}>
            {confirming && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {confirming ? t("resetPassword.submitting") : t("resetPassword.submit")}
          </Button>

          <div className="text-center">
            <Link to={ROUTES.LOGIN} className="text-sm text-muted-foreground hover:underline">
              {t("resetPassword.loginLink")}
            </Link>
          </div>
        </form>
      )}
    </LoginLayout>
  );
}
