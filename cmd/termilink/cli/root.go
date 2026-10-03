package cli

import (
	"github.com/spf13/cobra"

	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/version"
)

func NewRootCmd() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:           "termilink",
		Short:         "Control your computer from Telegram",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to the configuration file")

	root.AddCommand(
		newStartCmd(&configPath),
		newStatusCmd(&configPath),
		newConfigCmd(&configPath),
		newProjectsCmd(&configPath),
		newSessionsCmd(&configPath),
		newAuditCmd(&configPath),
		newServiceCmd(&configPath),
		newInitCmd(&configPath),
	)
	return root
}
