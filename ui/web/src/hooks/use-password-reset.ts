import { useCallback, useState } from "react";
import { useHttp } from "@/hooks/use-ws";
import type { ApiError } from "@/api/errors";

interface UsePasswordResetResult {
  requesting: boolean;
  confirming: boolean;
  error: ApiError | null;
  /** Requests a password-reset email. Always resolves successfully (no enumeration). */
  request: (email: string) => Promise<void>;
  /** Confirms a reset using token + new password. Throws ApiError on failure. */
  confirm: (token: string, newPassword: string) => Promise<void>;
}

export function usePasswordReset(): UsePasswordResetResult {
  const http = useHttp();
  const [requesting, setRequesting] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const request = useCallback(
    async (email: string) => {
      setRequesting(true);
      setError(null);
      try {
        await http.post<void>("/v1/auth/password-reset/request", { email });
      } catch (err) {
        // Per BE no-enumeration policy this should rarely error (returns 204
        // for both valid and invalid emails). Treat 4xx-ish faults as a real
        // error so we can show rate-limit / network messages.
        setError(err as ApiError);
        throw err;
      } finally {
        setRequesting(false);
      }
    },
    [http],
  );

  const confirm = useCallback(
    async (token: string, newPassword: string) => {
      setConfirming(true);
      setError(null);
      try {
        await http.post<void>("/v1/auth/password-reset/confirm", {
          token,
          new_password: newPassword,
        });
      } catch (err) {
        setError(err as ApiError);
        throw err;
      } finally {
        setConfirming(false);
      }
    },
    [http],
  );

  return { requesting, confirming, error, request, confirm };
}
