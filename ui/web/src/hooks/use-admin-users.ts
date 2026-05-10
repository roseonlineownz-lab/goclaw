import { useCallback, useState } from "react";
import { useHttp } from "@/hooks/use-ws";
import type { ApiError } from "@/api/errors";
import type { AdminCreateUserData } from "@/schemas/admin-create-user.schema";

export interface AdminUser {
  id: string;
  email: string;
  display_name: string | null;
  role: string;
  status: string;
  created_at: string;
  updated_at: string;
}

interface UseAdminUsersResult {
  users: AdminUser[];
  loading: boolean;
  error: ApiError | null;
  load: () => Promise<void>;
  createUser: (data: AdminCreateUserData) => Promise<AdminUser>;
}

export function useAdminUsers(): UseAdminUsersResult {
  const http = useHttp();
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await http.get<{ users: AdminUser[] }>("/v1/users");
      setUsers(res.users ?? []);
    } catch (err) {
      setError(err as ApiError);
    } finally {
      setLoading(false);
    }
  }, [http]);

  const createUser = useCallback(
    async (data: AdminCreateUserData) => {
      setError(null);
      try {
        const created = await http.post<AdminUser>("/v1/users", {
          email: data.email,
          display_name: data.display_name,
          password: data.password,
          role: data.role,
        });
        setUsers((prev) => [created, ...prev]);
        return created;
      } catch (err) {
        // Mirror usePasswordReset.confirm: surface error on the hook AND throw
        // so callers may either render error state or rely on the catch block.
        setError(err as ApiError);
        throw err;
      }
    },
    [http],
  );

  return { users, loading, error, load, createUser };
}
