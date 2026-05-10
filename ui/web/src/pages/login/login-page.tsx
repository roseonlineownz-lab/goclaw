import { useEffect, useState } from "react";
import { Link, Navigate, useNavigate, useLocation } from "react-router";
import { useTranslation } from "react-i18next";
import { useAuthStore } from "@/stores/use-auth-store";
import { ROUTES } from "@/lib/constants";
import { LoginLayout } from "./login-layout";
import { LoginTabs, type LoginMode } from "./login-tabs";
import { TokenForm } from "./token-form";
import { PairingForm } from "./pairing-form";

export function LoginPage() {
  const { t } = useTranslation("login");
  const [mode, setMode] = useState<LoginMode>("token");

  const setCredentials = useAuthStore((s) => s.setCredentials);
  const setPairing = useAuthStore((s) => s.setPairing);
  const navigate = useNavigate();
  const location = useLocation();

  const from =
    (location.state as { from?: { pathname: string } })?.from?.pathname ??
    ROUTES.OVERVIEW;

  function handleTokenLogin(userId: string, token: string) {
    setCredentials(token, userId);
    navigate(from, { replace: true });
  }

  function handlePairingApproved(senderID: string, userId: string) {
    setPairing(senderID, userId);
    setTimeout(() => navigate(from, { replace: true }), 500);
  }

  return (
    <LoginLayout subtitle={t("login.subtitle")}>
      <h2 className="text-center text-lg font-semibold">{t("login.title")}</h2>
      <PasswordForm
        onSubmit={async (email, password) => {
          await login(email, password);
          navigate(from, { replace: true });
        }}
      />
      <div className="text-center">
        <Link
          to={ROUTES.FORGOT_PASSWORD}
          className="text-sm text-muted-foreground hover:text-foreground hover:underline"
        >
          {t("forgotPassword.title")}?
        </Link>
      </div>
    </LoginLayout>
  );
}
