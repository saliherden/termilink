package service

import (
	"context"
	"fmt"
	"log/slog"

	tg "github.com/go-telegram/bot"

	"github.com/saliherden/termilink/internal/audit"
	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/security"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/telegram"
	"github.com/saliherden/termilink/internal/terminal"
)

// extraBotOptions carries no options in production, and exists so tests can
// reach the rest of New. tg.New calls getMe while constructing the client, so
// without tg.WithSkipGetMe() the wiring below is only reachable with the real
// Telegram API and a real token.
var extraBotOptions []tg.Option

type Service struct {
	cfg      *config.Config
	bot      *tg.Bot
	logger   *slog.Logger
	sessions *session.Manager
	handler  *telegram.Handler
	audit    *audit.Logger
}

func New(cfg *config.Config, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}

	authorizer := security.New(cfg.Security.Owner, cfg.Security.AllowedUsers)
	policy, err := security.NewPolicy(cfg.Security.ApproveDangerous, cfg.Security.DangerousPatterns, cfg.Workspace.Allowed)
	if err != nil {
		return nil, fmt.Errorf("build security policy: %w", err)
	}
	runner := terminal.NewRunner(
		cfg.Terminal.Shell,
		cfg.Terminal.MaxOutputBytes,
	)
	// Manager.Save is a no-op without a state file, so an unresolvable home
	// would start a gateway that looks healthy and forgets every session on
	// restart. The audit log already refuses to start in that case; this keeps
	// the session state from being the one silent exception. state_file in the
	// config is what makes this work under a service account that has no
	// usable home directory.
	stateFile, err := session.ResolveStateFile(cfg.StateFile)
	if err != nil {
		return nil, fmt.Errorf("resolve session state file: %w", err)
	}
	sessions := session.NewManagerWithStateFile(stateFile)

	auditLogger, err := newAuditLogger(cfg)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}

	handler := telegram.NewHandler(telegram.Options{
		Authorizer:   authorizer,
		Policy:       policy,
		Runner:       runner,
		Sessions:     sessions,
		Projects:     cfg.Projects,
		Timeout:      cfg.Terminal.CommandTimeout.Std(),
		Logger:       logger,
		Audit:        auditLogger,
		MaxFileBytes: cfg.Telegram.MaxFileBytes,
		BigFileLink:  cfg.Telegram.BigFileLinkHost,
		Agent:        cfg.Agent,
	})

	// The library reports polling failures to its own handler, which defaults
	// to a bare log.Printf on stderr. Routing them into the app's logger keeps
	// them in the same structured stream as everything else, so an outage that
	// started after boot is visible in one place. It retries forever rather
	// than giving up, and the backoff tops out at five seconds, so this cannot
	// flood the log.
	opts := append([]tg.Option{
		tg.WithDefaultHandler(handler.Callback()),
		tg.WithErrorsHandler(func(err error) {
			logger.Warn("telegram api error", "err", err)
		}),
	}, extraBotOptions...)
	bot, err := tg.New(cfg.Telegram.BotToken, opts...)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	return &Service{
		cfg:      cfg,
		bot:      bot,
		logger:   logger,
		sessions: sessions,
		handler:  handler,
		audit:    auditLogger,
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("termilink agent started",
		"allowed_users", len(s.cfg.Security.AllowedUsers),
		"projects", len(s.cfg.Projects),
		"shell", s.cfg.Terminal.Shell,
		"audit", s.auditPath(),
		"state", s.sessions.StateFile(),
	)
	defer s.handler.Close()
	s.bot.Start(ctx)
	if err := s.sessions.Save(); err != nil {
		s.logger.Warn("could not persist session state on shutdown", "err", err)
	}
	s.logger.Info("termilink agent stopped")
	return nil
}

// auditPath returns the configured audit log path for logging purposes, or
// "off" when auditing is disabled.
func (s *Service) auditPath() string {
	if s.audit == nil {
		return "off"
	}
	return s.audit.Path()
}

// newAuditLogger builds the audit logger from config. An empty audit_log
// resolves to the default location; "off" disables auditing (nil logger).
func newAuditLogger(cfg *config.Config) (*audit.Logger, error) {
	path, enabled := audit.PathFor(cfg.Security.AuditLog)
	if !enabled {
		return nil, nil
	}
	return audit.OpenWithOptions(path, cfg.Security.AuditMaxBytes, cfg.Security.AuditKeep)
}
