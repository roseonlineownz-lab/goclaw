import { create } from "zustand";
import { persist } from "zustand/middleware";
import i18n from "@/i18n";
import { type Language } from "@/lib/constants";

export type Theme = "light" | "dark" | "system";

interface UiState {
  theme: Theme;
  language: Language;
  timezone: string; // IANA timezone or "auto"
  sidebarCollapsed: boolean;
  mobileSidebarOpen: boolean;

  setTheme: (theme: Theme) => void;
  setLanguage: (language: Language) => void;
  setTimezone: (tz: string) => void;
  toggleSidebar: () => void;
  setSidebarCollapsed: (collapsed: boolean) => void;
  setMobileSidebarOpen: (open: boolean) => void;
}

export const useUiStore = create<UiState>()(
  persist(
    (set, get) => ({
      theme: "dark" as Theme,
      language: (i18n.language as Language) ?? "en",
      timezone: "auto",
      sidebarCollapsed: false,
      mobileSidebarOpen: false,

      setTheme: (theme) => {
        set({ theme });
      },

      setLanguage: (language) => {
        i18n.changeLanguage(language);
        set({ language });
      },

      setTimezone: (tz) => {
        set({ timezone: tz });
      },

      toggleSidebar: () => {
        set({ sidebarCollapsed: !get().sidebarCollapsed });
      },

      setSidebarCollapsed: (collapsed) => {
        set({ sidebarCollapsed: collapsed });
      },

      setMobileSidebarOpen: (open) => set({ mobileSidebarOpen: open }),
    }),
    {
      name: "goclaw:ui",
      version: 1,
      // v0 → v1: drop tenant-scoped UI preferences (last-active-tenant-id, per-tenant prefs).
      migrate: (persisted, oldVersion) => {
        if (!persisted || typeof persisted !== "object") return persisted;
        if (oldVersion < 1) {
          const s = persisted as Record<string, unknown>;
          delete s.activeTenantId;
          delete s.lastTenantId;
          delete s.tenantPrefs;
        }
        return persisted;
      },
      partialize: (state) => ({
        theme: state.theme,
        language: state.language,
        timezone: state.timezone,
        sidebarCollapsed: state.sidebarCollapsed,
      }),
    }
  )
);
