package cli

import (
	"github.com/spf13/cobra"

	"github.com/saliherden/termilink/internal/version"
)

func NewRootCmd() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:           "termilink",
		Short:         "Remote terminal access and development automation for your own computer",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "path to the configuration file")

	root.AddCommand(
		newStartCmd(&configPath),
		newStatusCmd(&configPath),
		newConfigCmd(&configPath),
		newProjectsCmd(&configPath),
		newSessionsCmd(),
		newAuditCmd(&configPath),
		newInitCmd(&configPath),
	)
	return root
}
