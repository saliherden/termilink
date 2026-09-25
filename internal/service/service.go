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
		cfg.Terminal.CommandTimeout.Std(),
		cfg.Terminal.MaxOutputBytes,
	)
	sessions := session.NewManagerWithStateFile(session.DefaultStateFile())

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
	})

	bot, err := tg.New(cfg.Telegram.BotToken, tg.WithDefaultHandler(handler.Callback()))
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
	)
	defer s.handler.Close()
	s.bot.Start(ctx)
	s.sessions.Save()
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
	if cfg.Security.AuditLog == "off" {
		return nil, nil
	}
	return audit.Open(cfg.Security.AuditLog)
}
