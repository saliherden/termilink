package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/saliherden/termilink/internal/instance"
	"github.com/saliherden/termilink/internal/service"
)

func newStartCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "start the agent (Telegram gateway)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}

			release, err := instance.Acquire(instance.DefaultLockPath())
			if err != nil {
				return err
			}
			defer func() { _ = release() }()

			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
			s, err := service.New(cfg, logger)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			logger.Info("starting telegram gateway", "config", cfg.Path)
			if err := s.Run(ctx); err != nil {
				return fmt.Errorf("agent failed: %w", err)
			}
			return nil
		},
	}
}
