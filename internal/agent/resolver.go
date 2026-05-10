package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/eventbus"
	"github.com/nextlevelbuilder/goclaw/internal/hooks"
	"github.com/nextlevelbuilder/goclaw/internal/memory"
	mcpbridge "github.com/nextlevelbuilder/goclaw/internal/mcp"
	"github.com/nextlevelbuilder/goclaw/internal/media"
	"github.com/nextlevelbuilder/goclaw/internal/providerresolve"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/sandbox"
	"github.com/nextlevelbuilder/goclaw/internal/skills"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

// ResolverDeps holds shared dependencies for the agent resolver.
type ResolverDeps struct {
	AgentStore     store.AgentStore
	ProviderStore  store.ProviderStore
	ProviderReg    *providers.Registry
	ModelRegistry  providers.ModelRegistry // per-model context window + capabilities lookup
	Bus            bus.EventPublisher
	Sessions       store.SessionStore
	Tools          *tools.Registry
	ToolPolicy     *tools.PolicyEngine
	Skills         *skills.Loader
	HasMemory      bool
	OnEvent        func(AgentEvent)
	TraceCollector *tracing.Collector

	// Per-user profile + file seeding + dynamic context loading
	EnsureUserProfile EnsureUserProfileFunc
	SeedUserFiles     SeedUserFilesFunc
	ContextFileLoader ContextFileLoaderFunc
	BootstrapCleanup  BootstrapCleanupFunc
	CacheInvalidate   CacheInvalidateFunc
	DefaultTimezone   string // system default timezone for bootstrap pre-fill

	// Security
	InjectionAction string // "log", "warn", "block", "off"
	MaxMessageChars int

	// Global defaults (from config.json) — per-agent DB overrides take priority
	CompactionCfg          *config.CompactionConfig
	ContextPruningCfg      *config.ContextPruningConfig
	SandboxEnabled         bool
	SandboxContainerDir    string
	SandboxWorkspaceAccess string

	// Inter-agent delegation
	AgentLinkStore store.AgentLinkStore

	// Agent teams
	TeamStore store.TeamStore
	DataDir   string // global workspace root for team workspace resolution

	// Secure CLI credential store for credentialed exec
	SecureCLIStore store.SecureCLIStore

	// Builtin tool settings
	BuiltinToolStore store.BuiltinToolStore

	// MCP server store — for per-agent MCP tool loading
	MCPStore store.MCPServerStore

	// Shared MCP connection pool — eliminates duplicate connections across agents
	MCPPool *mcpbridge.Pool

	// MCP grant checker — for runtime grant verification at BridgeTool.Execute
	MCPGrantChecker mcpbridge.GrantChecker

	// Skill access store — for per-agent skill visibility filtering
	SkillAccessStore store.SkillAccessStore

	// Config permission store for group file writer checks
	ConfigPermStore store.ConfigPermissionStore

	// Persistent media storage for cross-turn image/document access
	MediaStore *media.Store

	// Model pricing for cost tracking
	ModelPricing map[string]*config.ModelPricing

	// Tracing store for budget enforcement queries
	TracingStore store.TracingStore

	// Memory store for extractive memory fallback
	MemoryStore store.MemoryStore

	// V3 evolution metrics store
	EvolutionMetricsStore store.EvolutionMetricsStore

	// Contact store for user identity resolution (channel contacts).
	ContactStore store.ContactStore

	// Project store for project metadata lookups (slug → workspace path).
	ProjectStore store.ProjectStore

	// Users store for users.user_key lookups during channel workspace
	// resolution (12-scenario matrix: web + merged-contact paths).
	UsersStore store.UsersStore

	// System config store for global settings (allowed_paths, etc.)
	SystemConfigs store.SystemConfigStore

	// Global workspace root (GOCLAW_WORKSPACE)
	Workspace string

	// TTS auto mode from config: "off", "always", "inbound", "tagged"
	TTSAutoMode string

	// V3 auto-inject: episodic memory injection into system prompt (nil = disabled)
	AutoInjector memory.AutoInjector

	// V3 domain event bus for consolidation pipeline (nil = disabled)
	DomainBus eventbus.DomainEventBus

	// HookDispatcher fires lifecycle hook events (Issue #875). Nil = noop.
	HookDispatcher hooks.Dispatcher

	// Vault hook: called when a text file is uploaded by user (nil = no vault registration)
	OnTextUploaded func(ctx context.Context, path, content string)
}

// NewManagedResolver creates a ResolverFunc that builds Loops from DB agent data.
// Agents are defined in Postgres, not config.json.
func NewManagedResolver(deps ResolverDeps) ResolverFunc {
	return func(ctx context.Context, agentKey string) (Agent, error) {

		// Support lookup by UUID (e.g. from cron jobs that store agent_id as UUID)
		var ag *store.AgentData
		var err error
		if id, parseErr := uuid.Parse(agentKey); parseErr == nil {
			ag, err = deps.AgentStore.GetByID(ctx, id)
		} else {
			ag, err = deps.AgentStore.GetByKey(ctx, agentKey)
		}
		if err != nil {
			return nil, fmt.Errorf("agent not found: %s", agentKey)
		}

		if ag.Status != store.AgentStatusActive {
			return nil, fmt.Errorf("agent %s is inactive", agentKey)
		}

		// Resolve provider (tenant-aware: tries tenant-specific first, falls back to master)
		provider, err := providerresolve.ResolveConfiguredProvider(deps.ProviderReg, ag)
		if err != nil {
			// Fallback to any available provider
			names := deps.ProviderReg.List()
			if len(names) == 0 {
				return nil, fmt.Errorf("no providers configured for agent %s", agentKey)
			}
			provider, _ = deps.ProviderReg.GetByName(names[0])
			slog.Warn("agent provider not found, using fallback",
				"agent", agentKey, "wanted", ag.Provider, "using", names[0])
			if rc := ag.ParseReasoningConfig(); rc.Effort != "" && rc.Effort != "off" {
				slog.Warn("agent thinking may not be supported by fallback provider",
					"agent", agentKey, "thinking_level", rc.Effort,
					"wanted_provider", ag.Provider, "fallback_provider", names[0])
			}
		}

		if provider == nil {
			return nil, fmt.Errorf("no provider available for agent %s", agentKey)
		}
		providerReasoningDefaults := (*store.ProviderReasoningConfig)(nil)
		if deps.ProviderStore != nil {
			if providerData, err := deps.ProviderStore.GetProviderByName(ctx, provider.Name()); err == nil && providerData != nil {
				providerReasoningDefaults = store.ParseProviderReasoningConfig(providerData.Settings)
			}
		}

		// Load bootstrap files from DB
		contextFiles := bootstrap.LoadFromStore(ctx, deps.AgentStore, ag.ID)

		// Inject TEAM.md for all team members (lead + members) so every agent
		// knows the team workflow: create/claim/complete tasks via team_tasks tool.
		hasTeam := false
		isTeamLead := false
		if deps.TeamStore != nil {
			hasTeamMD := false
			for _, cf := range contextFiles {
				if cf.Path == bootstrap.TeamFile {
					hasTeamMD = true
					break
				}
			}
			if !hasTeamMD {
				if team, err := deps.TeamStore.GetTeamForAgent(ctx, ag.ID); err == nil && team != nil {
					if members, err := deps.TeamStore.ListMembers(ctx, team.ID); err == nil {
						hasTeam = true
						contextFiles = append(contextFiles, bootstrap.ContextFile{
							Path:    bootstrap.TeamFile,
							Content: buildTeamMD(team, members, ag.ID),
						})
						// Detect lead role for tool policy
						for _, m := range members {
							if m.AgentID == ag.ID && m.Role == store.TeamRoleLead {
								isTeamLead = true
								break
							}
						}
					}
				}
			} else {
				hasTeam = true
			}
		}

		// Inject negative context so the model doesn't waste iterations probing
		// unavailable capabilities (team_tasks, etc.).
		if !hasTeam {
			contextFiles = append(contextFiles, bootstrap.ContextFile{
				Path:    bootstrap.AvailabilityFile,
				Content: "You are NOT part of any team. Do not use team_tasks tool.",
			})
		}

		contextWindow := ag.ContextWindow
		if contextWindow <= 0 {
			contextWindow = config.DefaultContextWindow
		}
		maxIter := ag.MaxToolIterations
		if maxIter <= 0 {
			maxIter = config.DefaultMaxIterations
		}

		// Per-agent config overrides (fallback to global defaults from config.json)
		compactionCfg := deps.CompactionCfg
		if c := ag.ParseCompactionConfig(); c != nil {
			compactionCfg = c
		}
		contextPruningCfg := deps.ContextPruningCfg
		if c := ag.ParseContextPruning(); c != nil {
			contextPruningCfg = c
		}
		sandboxEnabled := deps.SandboxEnabled
		sandboxContainerDir := deps.SandboxContainerDir
		sandboxWorkspaceAccess := deps.SandboxWorkspaceAccess
		var sandboxCfgOverride *sandbox.Config
		if c := ag.ParseSandboxConfig(); c != nil {
			resolved := c.ToSandboxConfig()
			sandboxContainerDir = resolved.ContainerWorkdir()
			sandboxWorkspaceAccess = string(resolved.WorkspaceAccess)
			sandboxCfgOverride = &resolved
		}

		// Expand ~ in workspace path and ensure directory exists.
		workspace := ag.Workspace
		if workspace != "" {
			workspace = config.ExpandHome(workspace)
			if !filepath.IsAbs(workspace) {
				workspace, _ = filepath.Abs(workspace)
			}
		}
		// Fallback to global workspace if per-agent workspace is empty
		if workspace == "" && deps.Workspace != "" {
			workspace = deps.Workspace
		}
		if workspace != "" {
			if err := os.MkdirAll(workspace, 0755); err != nil {
				slog.Warn("failed to create agent workspace directory", "workspace", workspace, "agent", agentKey, "error", err)
			}
		}

		toolsReg := deps.Tools

		// Per-agent MCP servers: connect to granted MCP servers and register their tools.
		// Uses a per-agent MCP Manager that queries the MCPServerStore for accessible servers.
		//
		// IMPORTANT: Always clone the registry before MCP registration to prevent
		// cross-agent tool leaks. Without cloning, MCP BridgeTools registered for
		// one agent pollute the shared deps. Tools and become visible to ALL agents
		// (even those without MCP grants), because FilterTools reads from registry.List().
		hasMCPTools := false
		var mcpUserCredSrvs []store.MCPAccessInfo
		if deps.MCPStore != nil {
			if toolsReg == deps.Tools {
				toolsReg = deps.Tools.Clone()
			}
			var mcpOpts []mcpbridge.ManagerOption
			mcpOpts = append(mcpOpts, mcpbridge.WithStore(deps.MCPStore))
			if deps.MCPPool != nil {
				mcpOpts = append(mcpOpts, mcpbridge.WithPool(deps.MCPPool))
			}
			if deps.MCPGrantChecker != nil {
				mcpOpts = append(mcpOpts, mcpbridge.WithGrantChecker(deps.MCPGrantChecker))
			}
			mcpMgr := mcpbridge.NewManager(toolsReg, mcpOpts...)
			if err := mcpMgr.LoadForAgent(ctx, ag.ID, ""); err != nil {
				slog.Warn("failed to load MCP servers for agent", "agent", agentKey, "error", err)
			} else {
				mcpUserCredSrvs = mcpMgr.UserCredServers()
				// User-credential servers (Notion, etc.) are deferred at startup
				// but will produce tools per-request via getUserMCPTools.
				// Set flag so agentToolPolicyWithMCP injects "group:mcp" into alsoAllow.
				if len(mcpUserCredSrvs) > 0 {
					hasMCPTools = true
				}
				if mcpMgr.IsSearchMode() {
					// Search mode: too many tools — register mcp_tool_search meta-tool.
					// Also wire lazy activator so deferred tools can be called by name directly.
					toolsReg.SetDeferredActivator(mcpMgr.ActivateToolIfDeferred)
					searchTool := mcpbridge.NewMCPToolSearchTool(mcpMgr)
					toolsReg.Register(searchTool)
					hasMCPTools = true
					slog.Info("mcp.agent.search_mode", "agent", agentKey,
						"deferred_tools", len(mcpMgr.DeferredToolInfos()))
				} else {
					toolNames := mcpMgr.ToolNames()
					if len(toolNames) > 0 {
						hasMCPTools = true
						slog.Info("mcp.agent.tools_loaded", "agent", agentKey, "tools", len(toolNames))
					}
				}
			}
		}

		// Per-agent memory: enabled if global memory manager exists AND
		// per-agent config doesn't explicitly disable it.
		hasMemory := deps.HasMemory
		if mc := ag.ParseMemoryConfig(); mc != nil && mc.Enabled != nil {
			if !*mc.Enabled {
				hasMemory = false
			}
		}

		// Load global builtin tool settings from DB (for settings cascade)
		var builtinSettings tools.BuiltinToolSettings
		if deps.BuiltinToolStore != nil {
			if allTools, err := deps.BuiltinToolStore.List(ctx); err == nil {
				builtinSettings = make(tools.BuiltinToolSettings, len(allTools))
				for _, t := range allTools {
					if len(t.Settings) > 0 && string(t.Settings) != "{}" {
						builtinSettings[t.Name] = []byte(t.Settings)
					}
				}
			}
		}

		// Load per-tenant tool exclusions (disabled tools for this agent's tenant)
		// Per-tenant tool settings overlay removed in v4 (single-tenant model).
		// Cascade now: agent-level tools_config → builtin defaults.
		var (
			disabledTools      map[string]bool
			tenantToolSettings tools.BuiltinToolSettings
		)

		// Load global allowed_paths (from system_configs).
		// These extend filesystem tool access beyond the agent's workspace.
		var tenantAllowedPaths []string
		if deps.SystemConfigs != nil {
			if raw, err := deps.SystemConfigs.Get(ctx, "allowed_paths"); err == nil && raw != "" {
				if json.Unmarshal([]byte(raw), &tenantAllowedPaths) == nil && len(tenantAllowedPaths) > 0 {
					for i, p := range tenantAllowedPaths {
						tenantAllowedPaths[i] = config.ExpandHome(p)
					}
					slog.Debug("allowed paths loaded", "agent", agentKey, "paths", len(tenantAllowedPaths))
				}
			}
		}

		// Filter skills by visibility + agent grants.
		// Only public skills and explicitly granted internal skills appear in the system prompt.
		var skillAllowList []string
		if deps.SkillAccessStore != nil {
			if accessible, err := deps.SkillAccessStore.ListAccessible(ctx, ag.ID, ""); err == nil {
				skillAllowList = make([]string, 0, len(accessible))
				for _, sk := range accessible {
					skillAllowList = append(skillAllowList, sk.Slug)
				}
				slog.Debug("skill visibility filter", "agent", agentKey, "accessible", len(skillAllowList))
			} else {
				slog.Warn("failed to load accessible skills, falling back to all", "agent", agentKey, "error", err)
				// nil = fallback to all (better than blocking all skills)
			}
		}

		dataDir := deps.DataDir

		// v3 feature flags (from other_config JSONB).
		// NOTE: flags are immutable per-Loop — changes via admin API take effect on next session only.
		// In-flight loops continue with the flags set at creation. This is by design:
		// CacheKindAgent invalidation destroys the old Loop, and the next request creates a new one.
		v3f := ag.ParseV3Flags()

		// v3 orchestration mode: resolve from team membership + agent links
		orchMode := ResolveOrchestrationMode(ctx, ag.ID, deps.TeamStore, deps.AgentLinkStore)

		// Populate delegation targets for prompt injection (only when mode >= delegate).
		var delegateTargets []DelegateTargetEntry
		if orchMode != ModeSpawn && deps.AgentLinkStore != nil {
			if links, err := deps.AgentLinkStore.DelegateTargets(ctx, ag.ID); err == nil {
				for _, link := range links {
					delegateTargets = append(delegateTargets, DelegateTargetEntry{
						AgentKey:    link.TargetAgentKey,
						DisplayName: link.TargetDisplayName,
						Description: link.Description,
					})
				}
			}
		}

		// v3 evolution metrics: only wire store when feature flag enabled
		var evoMetricsStore store.EvolutionMetricsStore
		if v3f.EvolutionMetrics && deps.EvolutionMetricsStore != nil {
			evoMetricsStore = deps.EvolutionMetricsStore
		}

		restrictVal := true // always restrict agents to their workspace
		loop := NewLoop(LoopConfig{
			ID:                     ag.AgentKey,
			DisplayName:            ag.DisplayName,
			AgentUUID:              ag.ID,
			AgentOtherConfig:       ag.OtherConfig,
			IsTeamLead:             isTeamLead,
			AutoInjector:          deps.AutoInjector,
			Provider:               provider,
			Model:                  ag.Model,
			ModelRegistry:          deps.ModelRegistry,
			ContextWindow:          contextWindow,
			MaxTokens:              ag.ParseMaxTokens(),
			MaxIterations:          maxIter,
			Workspace:              workspace,
			DataDir:                dataDir,
			RestrictToWs:           &restrictVal,
			SubagentsCfg:           ag.ParseSubagentsConfig(),
			MemoryCfg:              ag.ParseMemoryConfig(),
			SandboxCfg:             sandboxCfgOverride,
			Bus:                    deps.Bus,
			DomainBus:              deps.DomainBus,
			HookDispatcher:         deps.HookDispatcher,
			Sessions:               deps.Sessions,
			Tools:                  toolsReg,
			ToolPolicy:             deps.ToolPolicy,
			AgentToolPolicy:        agentToolPolicyForTeam(agentToolPolicyWithWorkspace(agentToolPolicyWithMCP(ag.ParseToolsConfig(), hasMCPTools), hasTeam), isTeamLead),
			SkillsLoader:           deps.Skills,
			SkillAllowList:         skillAllowList,
			HasMemory:              hasMemory,
			ContextFiles:           contextFiles,
			EnsureUserProfile:      deps.EnsureUserProfile,
			SeedUserFiles:          deps.SeedUserFiles,
			ContextFileLoader:      deps.ContextFileLoader,
			BootstrapCleanup:       deps.BootstrapCleanup,
			CacheInvalidate:        deps.CacheInvalidate,
			DefaultTimezone:        deps.DefaultTimezone,
			OnEvent:                deps.OnEvent,
			TraceCollector:         deps.TraceCollector,
			InjectionAction:        deps.InjectionAction,
			MaxMessageChars:        deps.MaxMessageChars,
			CompactionCfg:          compactionCfg,
			ContextPruningCfg:      contextPruningCfg,
			SandboxEnabled:         sandboxEnabled,
			SandboxContainerDir:    sandboxContainerDir,
			SandboxWorkspaceAccess: sandboxWorkspaceAccess,
			BuiltinToolSettings:    builtinSettings,
			TenantToolSettings:     tenantToolSettings,
			TenantAllowedPaths:     tenantAllowedPaths,
			DisabledTools:          disabledTools,
			ReasoningConfig:        store.ResolveEffectiveReasoningConfig(providerReasoningDefaults, ag.ParseReasoningConfig()),
			PromptMode:             PromptMode(ag.ParsePromptMode()),
			PinnedSkills:           ag.ParsePinnedSkills(),
			SelfEvolve:             ag.ParseSelfEvolve(),
			AllowImageGeneration:   ag.ParseAllowImageGeneration(),
			TTSAutoMode:            deps.TTSAutoMode,
			SkillEvolve:            ag.ParseSkillEvolve(),
			SkillNudgeInterval:     ag.ParseSkillNudgeInterval(),
			ShareWorkspace:         ag.ShareWorkspace,
			ShareMemory:            ag.ShareMemory,
			ShellDenyGroups:        ag.ParseShellDenyGroups(),
			ConfigPermStore:        deps.ConfigPermStore,
			TeamStore:              deps.TeamStore,
			SecureCLIStore:         deps.SecureCLIStore,
			OnTextUploaded:         deps.OnTextUploaded,
			MediaStore:             deps.MediaStore,
			ModelPricing:           deps.ModelPricing,
			BudgetMonthlyCents:     derefInt(ag.BudgetMonthlyCents),
			TracingStore:           deps.TracingStore,
			MemoryStore:            deps.MemoryStore,
			MCPStore:               deps.MCPStore,
			MCPPool:                deps.MCPPool,
			MCPUserCredSrvs:        mcpUserCredSrvs,
			MCPGrantChecker:        deps.MCPGrantChecker,
			OrchMode:               orchMode,
			DelegateTargets:        delegateTargets,
			EvolutionMetricsStore:  evoMetricsStore,
			UserResolver:           newContactResolver(deps.ContactStore),
			ContactStore:           deps.ContactStore,
			ProjectStore:           deps.ProjectStore,
			UsersStore:             deps.UsersStore,
		})

		slog.Info("resolved agent from DB", "agent", agentKey, "model", ag.Model, "provider", ag.Provider)
		return loop, nil
	}
}

// InvalidateAgent removes an agent from the router cache, forcing re-resolution.
// Used when agent config is updated via API. Empty agentKey is rejected to
// prevent accidental wildcard wipes (use InvalidateAll for that).
func (r *Router) InvalidateAgent(agentKey string) {
	if agentKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, agentKey)
	slog.Debug("invalidated agent cache", "agent", agentKey)
}

// InvalidateAll clears the entire agent cache, forcing all agents to re-resolve.
// Used when global tools change (custom tools reload).
func (r *Router) InvalidateAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = make(map[string]*agentEntry)
	slog.Debug("invalidated all agent caches")
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
