package cmd

import (
	"context"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/audio"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/gateway/methods"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func registerAllMethods(server *gateway.Server, agents *agent.Router, sessStore store.SessionStore, projectGrantStore store.ProjectGrantStore, cronStore store.CronStore, pairingStore store.PairingStore, cfg *config.Config, cfgPath, workspace, dataDir string, msgBus *bus.MessageBus, execApprovalMgr *tools.ExecApprovalManager, agentStore store.AgentStore, skillStore store.SkillStore, configSecretsStore store.ConfigSecretsStore, teamStore store.TeamStore, contextFileInterceptor *tools.ContextFileInterceptor, logTee *gateway.LogTee, heartbeatStore store.HeartbeatStore, configPermStore store.ConfigPermissionStore, sysConfigStore store.SystemConfigStore, audioMgr *audio.Manager, contactStore store.ContactStore, channelInstanceStore store.ChannelInstanceStore, projectStore store.ProjectStore, episodicStore store.EpisodicStore) (*methods.PairingMethods, *methods.HeartbeatMethods, *methods.ChatMethods, *methods.ConfigPermissionsMethods) {
	router := server.Router()

	// Phase 1: Core methods
	chatMethods := methods.NewChatMethods(agents, sessStore, cfg, server.RateLimiter(), msgBus)
	chatMethods.SetAudioManager(audioMgr) // Wire TTS auto-apply for WS responses
	chatMethods.Register(router)
	methods.NewAgentsMethods(agents, cfg, cfgPath, workspace, agentStore, contextFileInterceptor, msgBus).Register(router)
	sessionsMethods := methods.NewSessionsMethods(sessStore, projectGrantStore, msgBus, cfg)
	// Wire FS-side deps so sessions.updateProject relocates the session
	// subdir + retags session-scoped episodic memory atomically with the
	// DB binding flip. dataDir is the canonical workspace root.
	sessionsMethods.SetProjectSwitchDeps(projectStore, episodicStore, dataDir)
	sessionsMethods.Register(router)
	configMethods := methods.NewConfigMethods(cfg, cfgPath, configSecretsStore, msgBus)
	if sysConfigStore != nil {
		configMethods.SetSystemConfigSync(func(ctx context.Context, c *config.Config) {
			seedConfigForContext(ctx, sysConfigStore, c, false) // onlyMissing=false → upsert
			if msgBus != nil {
				msgBus.Broadcast(bus.Event{Name: bus.TopicSystemConfigChanged, Payload: context.Background()})
			}
		})
	}
	configMethods.Register(router)

	// Phase 2: Skills (uses SkillStore interface — PG or File)
	methods.NewSkillsMethods(skillStore).Register(router)

	// Phase 2: Cron (store created externally, shared with gateway)
	methods.NewCronMethods(cronStore, msgBus, cfg).Register(router)

	// Phase 2: Heartbeat
	heartbeatMethods := methods.NewHeartbeatMethods(heartbeatStore, msgBus)
	// Wire cache-aware resolver so heartbeat can accept agent_key or UUID
	// without a DB roundtrip on the hot path when the agent is router-cached.
	heartbeatMethods.SetAgentRouter(agents)
	heartbeatMethods.Register(router)

	// Phase 2: Config permissions
	cfgPerms := methods.NewConfigPermissionsMethods(configPermStore, agentStore)
	cfgPerms.SetAgentRouter(agents)
	cfgPerms.Register(router)

	// Phase 2: Pairing (store created externally, shared with channel manager).
	// OnApprove callback is set later by the caller after channel manager is created.
	pairingMethods := methods.NewPairingMethods(pairingStore, msgBus, server.RateLimiter())
	pairingMethods.Register(router)

	// Phase 2: Usage (queries SessionStore for real token data)
	methods.NewUsageMethods(sessStore).Register(router)

	// Phase 2: Exec approval (always registered — returns empty when manager is nil)
	methods.NewExecApprovalMethods(execApprovalMgr, msgBus).Register(router)

	// Phase 2: Send (outbound message routing)
	methods.NewSendMethods(msgBus).Register(router)

	// Phase 3: Live log tailing
	methods.NewLogsMethods(logTee).Register(router)

	// Channel contacts: set/clear default project binding.
	if contactStore != nil && channelInstanceStore != nil && projectStore != nil {
		methods.NewChannelContactsMethods(contactStore, channelInstanceStore, agentStore, projectStore, projectGrantStore, msgBus, cfg).Register(router)
	}

	// Projects + project grants RPC.
	if projectStore != nil && projectGrantStore != nil {
		methods.NewProjectsMethods(projectStore, projectGrantStore, msgBus, cfg).Register(router)
		methods.NewProjectGrantsMethods(projectStore, projectGrantStore, msgBus, cfg).Register(router)
	}

	slog.Info("registered all RPC methods",
		"phase1", []string{"chat", "agents", "sessions", "config"},
		"phase2", []string{"skills", "cron", "heartbeat", "pairing", "usage", "exec_approval", "send"},
	)

	return pairingMethods, heartbeatMethods, chatMethods, cfgPerms
}
