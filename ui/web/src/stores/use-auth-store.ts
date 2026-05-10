import { create } from "zustand";
import { persist } from "zustand/middleware";
import { LOCAL_STORAGE_KEYS } from "@/lib/constants";
import { clearSetupSkippedState } from "@/lib/setup-skip";
import type { TenantMembership } from "@/types/tenant";

type UserRole = "owner" | "admin" | "operator" | "viewer" | "";

interface AuthState {
  token: string;
  userId: string;
  senderID: string; // browser pairing: persistent device identity
  connected: boolean;
  role: UserRole; // server-assigned role from connect response
  serverInfo: { name?: string; version?: string } | null;
  tenantId: string;
  tenantName: string;
  tenantSlug: string;
  isOwner: boolean;
  availableTenants: TenantMembership[];
  tenantSelected: boolean; // true after user picks a tenant (or auto-selected)

  setCredentials: (token: string, userId: string) => void;
  setPairing: (senderID: string, userId: string) => void;
  setConnected: (connected: boolean, serverInfo?: { name?: string; version?: string }) => void;
  setRole: (role: UserRole) => void;
  setTenant: (id: string, name: string, slug: string, isOwner: boolean) => void;
  setAvailableTenants: (tenants: TenantMembership[]) => void;
  setTenantSelected: (selected: boolean) => void;
  logout: () => void;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: "",
      userId: "",
      senderID: "",
      connected: false,
      role: "" as UserRole,
      serverInfo: null,
      tenantId: "",
      tenantName: "",
      tenantSlug: "",
      isOwner: false,
      availableTenants: [],
      tenantSelected: !!localStorage.getItem(LOCAL_STORAGE_KEYS.TENANT_ID),

      setCredentials: (token, userId) => {
        set({ token, userId });
      },

      setPairing: (senderID, userId) => {
        set({ senderID, userId });
      },

      setConnected: (connected, serverInfo) => {
        set({ connected, serverInfo: serverInfo ?? null });
      },

      setRole: (role) => {
        set({ role });
      },

      setTenant: (id, name, slug, isOwner) => {
        set({ tenantId: id, tenantName: name, tenantSlug: slug, isOwner });
      },

      setAvailableTenants: (tenants) => {
        set({ availableTenants: tenants });
      },

      setTenantSelected: (selected) => {
        set({ tenantSelected: selected });
      },

      logout: () => {
        // Remove tenant scope keys that are still managed outside persist
        localStorage.removeItem("goclaw:tenant_id");
        localStorage.removeItem("goclaw:tenant_hint");
        clearSetupSkippedState();
        set({
          token: "", userId: "", senderID: "", connected: false, role: "", serverInfo: null,
          tenantId: "", tenantName: "", tenantSlug: "", isOwner: false, availableTenants: [],
          tenantSelected: false,
        });
      },
    }),
    {
      name: "goclaw:auth",
      version: 1,
      // v0 → v1: drop multi-tenant residue (tenantId, tenantSlug, tenants[]).
      // v3 stored these alongside credentials; v4 has no tenant scope.
      migrate: (persisted, oldVersion) => {
        if (!persisted || typeof persisted !== "object") return persisted;
        if (oldVersion < 1) {
          const s = persisted as Record<string, unknown>;
          delete s.tenantId;
          delete s.tenantSlug;
          delete s.tenants;
          delete s.activeTenantId;
        }
        return persisted;
      },
      partialize: (state) => ({
        token: state.token,
        userId: state.userId,
        senderID: state.senderID,
      }),
    }
  )
);
