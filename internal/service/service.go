package service

import (
	"context"
	"fmt"
	"log/slog"

	tg "github.com/go-telegram/bot"

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
}

func New(cfg *config.Config, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}

	authorizer := security.New(cfg.Security.Owner, cfg.Security.AllowedUsers)
	runner := terminal.NewRunner(
		cfg.Terminal.Shell,
		cfg.Terminal.CommandTimeout.Std(),
		cfg.Terminal.MaxOutputBytes,
	)
	sessions := session.NewManagerWithStateFile(session.DefaultStateFile())

	handler := telegram.NewHandler(telegram.Options{
		Authorizer: authorizer,
		Runner:     runner,
		Sessions:   sessions,
		Projects:   cfg.Projects,
		Timeout:    cfg.Terminal.CommandTimeout.Std(),
		Logger:     logger,
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
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("termilink agent started",
		"allowed_users", len(s.cfg.Security.AllowedUsers),
		"projects", len(s.cfg.Projects),
		"shell", s.cfg.Terminal.Shell,
	)
	defer s.handler.Close()
	s.bot.Start(ctx)
	s.sessions.Save()
	s.logger.Info("termilink agent stopped")
	return nil
}
