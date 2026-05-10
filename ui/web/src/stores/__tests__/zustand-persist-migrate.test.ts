import { describe, it, expect, beforeEach } from "vitest";

// Each test pre-populates localStorage with v0-shape (tenant residue) JSON
// for one of the three persisted stores, then imports the store fresh and
// asserts the migrated runtime shape has dropped tenant fields.

function seed(name: string, version: number, state: Record<string, unknown>) {
  localStorage.setItem(name, JSON.stringify({ state, version }));
}

beforeEach(() => {
  localStorage.clear();
});

describe("zustand persist migrations: v0 → v1", () => {
  it("auth store: drops tenantId / tenantSlug / tenants[] / activeTenantId", async () => {
    seed("goclaw:auth", 0, {
      token: "abc",
      refreshToken: "ref",
      userId: "u-1",
      senderID: "s-1",
      tenantId: "t-old",
      tenantSlug: "old-slug",
      tenants: [{ id: "t-old", slug: "old" }],
      activeTenantId: "t-old",
    });
    const mod = await import("@/stores/use-auth-store");
    const s = mod.useAuthStore.getState();
    expect(s.token).toBe("abc");
    expect(s.userId).toBe("u-1");
    const raw = localStorage.getItem("goclaw:auth")!;
    const parsed = JSON.parse(raw);
    expect(parsed.version).toBe(1);
    expect(parsed.state.tenantId).toBeUndefined();
    expect(parsed.state.tenantSlug).toBeUndefined();
    expect(parsed.state.tenants).toBeUndefined();
    expect(parsed.state.activeTenantId).toBeUndefined();
  });

  it("ui store: drops activeTenantId / lastTenantId / tenantPrefs", async () => {
    seed("goclaw:ui", 0, {
      theme: "dark",
      language: "en",
      timezone: "auto",
      sidebarCollapsed: false,
      pageSize: 20,
      activeTenantId: "t-old",
      lastTenantId: "t-old",
      tenantPrefs: { foo: "bar" },
    });
    const mod = await import("@/stores/use-ui-store");
    expect(mod.useUiStore.getState().theme).toBe("dark");
    const parsed = JSON.parse(localStorage.getItem("goclaw:ui")!);
    expect(parsed.version).toBe(1);
    expect(parsed.state.activeTenantId).toBeUndefined();
    expect(parsed.state.lastTenantId).toBeUndefined();
    expect(parsed.state.tenantPrefs).toBeUndefined();
  });

  it("team-event store: discards pre-v4 events on migration", async () => {
    seed("goclaw:recentEvents", 0, {
      events: [
        { id: 1, event: "old.event", payload: { tenant_id: "t-old" }, timestamp: 1, teamId: null, userId: null, chatId: null },
      ],
    });
    const mod = await import("@/stores/use-team-event-store");
    expect(mod.useTeamEventStore.getState().events).toEqual([]);
    const parsed = JSON.parse(localStorage.getItem("goclaw:recentEvents")!);
    expect(parsed.version).toBe(1);
    expect(parsed.state.events).toEqual([]);
  });
});
