package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saliherden/termilink/internal/audit"
	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/servicedef"
	"github.com/saliherden/termilink/internal/servicedef/launchd"
	"github.com/saliherden/termilink/internal/session"
)

// newServiceCmd builds the `termilink service` tree. Everything platform
// specific lives in service_darwin.go and service_other.go behind a build tag,
// so this file is identical on every system and the Linux CI runner compiles
// the same command surface macOS gets — minus the implementation.
func newServiceCmd(configPath *string) *cobra.Command {
	var (
		pathOverride string
		assumeYes    bool
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "service",
		Short: "install the agent as a system service",
		Long: `Install the agent as a background service managed by the operating system.

On macOS that means a launchd agent: it starts at login and restarts if it
crashes. The definition is generated from the resolved configuration, including
the PATH captured at install time, because a service job starts with a minimal
environment of its own.`,
	}

	cmd.PersistentFlags().StringVar(&pathOverride, "path", "", "use this PATH for the job instead of detecting one")
	cmd.PersistentFlags().BoolVarP(&assumeYes, "yes", "y", false, "do not ask for confirmation")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "print what would happen without writing anything")

	// build is for the commands that produce a definition and therefore have
	// something to explain: render and install. buildRemoval is for the commands
	// that only tear one down, where the PATH and the token are not being decided
	// and narrating them buries the one line the user asked for.
	build := func() (*servicedef.Service, servicedef.Paths, error) {
		return buildService(*configPath, pathOverride, true)
	}
	buildRemoval := func() (*servicedef.Service, servicedef.Paths, error) {
		return buildService(*configPath, pathOverride, false)
	}

	// render is the read-only half. It writes nothing anywhere, which is what
	// makes it safe in CI and what lets the generated definition be inspected
	// before an install.
	cmd.AddCommand(&cobra.Command{
		Use:   "render",
		Short: "print the service definition without installing it",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, paths, err := build()
			if err != nil {
				return err
			}
			def, err := renderService(svc, paths)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(def)
			return err
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "install and start the service",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, paths, err := build()
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Println("Dry run: no files were written and no service was loaded.")
				return describeService(svc, paths)
			}
			if !assumeYes {
				ok, err := confirmService(svc, paths)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Println("Cancelled. Nothing was written.")
					return nil
				}
			}
			return installService(svc, paths)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "stop and remove the service",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, paths, err := buildRemoval()
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Println("Dry run: the service was not stopped and nothing was removed.")
				return nil
			}
			// Ask only about something that exists. Prompting to delete a file
			// that was never written makes the command look like it has state
			// to clean up, and invites a reflexive y at the wrong moment.
			if _, err := os.Stat(paths.Plist); err != nil {
				if os.IsNotExist(err) {
					fmt.Printf("Not installed (no %s). Nothing to remove.\n", paths.Plist)
					return nil
				}
				return fmt.Errorf("stat %s: %w", paths.Plist, err)
			}
			if !assumeYes {
				fmt.Printf("Stop %s and delete %s?\n", svc.Label, paths.Plist)
				ok, err := confirm()
				if err != nil {
					return err
				}
				if !ok {
					fmt.Println("Cancelled. Nothing was removed.")
					return nil
				}
			}
			return uninstallService(svc, paths)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "show where the service is installed and whether it is running",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, paths, err := buildRemoval()
			if err != nil {
				return err
			}
			if err := describeService(svc, paths); err != nil {
				return err
			}
			// The resolved config paths are printed next to the service ones,
			// so a disagreement is visible before an install rather than
			// after a restart that lost its sessions.
			if cfg, err := loadConfig(*configPath); err == nil {
				fmt.Println()
				describeResolvedPaths(cfg)
			}
			fmt.Println()
			return serviceStatus(svc, paths)
		},
	})

	// path prints only the resolved PATH. That is the part which is hard to
	// reason about from a generated file, and the part that decides whether
	// the agent's shell can find the user's tools at all.
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "print the PATH the service job would run with",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			resolved, report, err := resolveJobPath(pathOverride)
			if err != nil {
				return err
			}
			if report.Source != "" {
				fmt.Println(report.String())
				fmt.Println()
			}
			fmt.Println(resolved)
			return nil
		},
	})

	return cmd
}

// buildService assembles the service description from the resolved
// configuration. It reads config.yaml rather than guessing, so a service runs
// the configuration the operator inspects with `termilink config`.
//
// explaining turns on the two reports — how the PATH was resolved and where the
// token came from. They are for the commands that write a definition and whose
// answer depends on both. `service path` exists to show the PATH on its own.
func buildService(configPath, pathOverride string, explaining bool) (*servicedef.Service, servicedef.Paths, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, servicedef.Paths{}, err
	}

	// The binary is the one running this command, resolved through symlinks so
	// the job does not depend on a PATH that a login session may not have.
	executable, err := os.Executable()
	if err != nil {
		return nil, servicedef.Paths{}, fmt.Errorf("locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}

	// The working directory is the config file's directory, not the process's:
	// the agent reads config.yaml and .env from there, and a service job starts
	// in $HOME. Getting this wrong starts the gateway with no configuration.
	workingDir, err := filepath.Abs(filepath.Dir(cfg.Path))
	if err != nil {
		return nil, servicedef.Paths{}, fmt.Errorf("resolve the configuration directory: %w", err)
	}

	// Resolve the state file through the same function the agent uses, so the
	// service and the running agent cannot end up on different files. A
	// relative or unresolvable path is refused here rather than becoming a job
	// that forgets every session on restart.
	stateFile, err := session.ResolveStateFile(cfg.StateFile)
	if err != nil {
		return nil, servicedef.Paths{}, fmt.Errorf("resolve session state file: %w", err)
	}

	label := servicedef.DefaultLabel
	jobPath, report, err := resolveJobPath(pathOverride)
	if err != nil {
		return nil, servicedef.Paths{}, err
	}
	// The report goes to stderr because `service render` writes the definition
	// to stdout and that has to stay pipeable into plutil.
	if explaining {
		fmt.Fprintln(os.Stderr, report.String())
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, servicedef.Paths{}, fmt.Errorf("no home directory: a service definition needs absolute paths")
	}
	paths := launchd.LogPaths(home, label)

	svc := servicedef.New(label, executable, workingDir, stateFile, paths.LogDir, []string{
		"PATH=" + jobPath,
		"TERM=xterm-256color",
	})

	if explaining {
		warnIfTokenNotPortable(cfg)
	}
	if err := validateServiceFiles(svc); err != nil {
		return nil, servicedef.Paths{}, err
	}
	return svc, paths, nil
}

// warnIfTokenNotPortable flags the one configuration that will not work under a
// service, rather than failing: the bot token lives in the environment the
// installer happened to run in, and a service job has no such environment.
//
// The token is deliberately not baked into the plist. A property list in
// ~/Library/LaunchAgents is world-readable, and it is generated by a command
// whose output is meant to be pasted and shared during review. The supported
// answer is a .env file in the working directory, which the agent already
// auto-loads and which the operator can create with 0600.
//
// The check is on the recorded origin rather than on the token's shape. Load has
// already substituted a `${TELEGRAM_BOT_TOKEN}` reference by the time this runs,
// so a token that came from the environment is indistinguishable from one that
// came from the file — and an earlier version of this looked for the redaction
// asterisks that only `termilink config` adds, which meant it could never fire.
func warnIfTokenNotPortable(cfg *config.Config) {
	switch cfg.Telegram.TokenSource {
	case "":
		// Load resolves the token or fails, so an empty source means something
		// upstream stopped believing it had one.
		fmt.Fprintln(os.Stderr, "warning: no bot token resolved; the agent will not be able to start.\n"+
			"         Put it in config.yaml, or in a .env file next to it (chmod 600).")
	case config.TokenSourceProcessEnv:
		fmt.Fprintln(os.Stderr, "warning: the bot token came from "+cfg.Telegram.TokenSource+", which a service\n"+
			"         job does not inherit, so the installed agent will start and fail to connect.\n"+
			"         Move it to a .env file next to config.yaml (chmod 600) and install again.")
	case config.TokenSourceDotEnv:
		fmt.Fprintln(os.Stderr, "note: the bot token is in "+cfg.Telegram.TokenSource+", which the service can read.\n"+
			"      Keep that file at 0600: it is the one thing that grants shell access.")
	}
}

// validateServiceFiles refuses a description whose inputs do not exist yet.
// Each of these produces a job that starts and then fails, which reads as a
// hung agent rather than a configuration mistake.
//
// It deliberately creates nothing. A read-only command that quietly makes a
// directory has already failed the one guarantee it exists to provide — that
// `render` and `--dry-run` can be run to look without touching the machine.
func validateServiceFiles(svc *servicedef.Service) error {
	if err := svc.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(svc.Executable); err != nil {
		return fmt.Errorf("the binary is not at %s (build it with `make build` first)", svc.Executable)
	}
	if _, err := os.Stat(svc.WorkingDir); err != nil {
		return fmt.Errorf("the configuration directory %s does not exist", svc.WorkingDir)
	}
	return nil
}

// resolveJobPath decides the PATH the job runs with, either from an explicit
// override or by inspecting the current environment and the login shell.
func resolveJobPath(override string) (string, servicedef.Report, error) {
	if override != "" {
		return override, servicedef.Report{Source: servicedef.SourceOverride}, nil
	}
	login, how, loginErr := loginShellPath()
	if loginErr != nil {
		// Not fatal: the inherited environment is often enough on its own,
		// and ResolvePath falls back to it. The report names the source used
		// either way, so a degraded resolution stays visible.
		fmt.Fprintf(os.Stderr, "could not read the login shell PATH: %v\n", loginErr)
	}
	resolved, report := servicedef.ResolvePath(os.Getenv("PATH"), login)
	// Only when the login shell's answer was the one used. Attributing it to a
	// PATH that came from the inherited environment would describe a shell that
	// never ran.
	if report.Source == servicedef.SourceLogin && how != "" {
		report.Note = how
	}
	return resolved, report, nil
}

// loginShellPath asks the user's login shell for its PATH.
//
// The interactive form (-lic) is deliberate. A non-interactive login shell does
// not read .zshrc, and on a machine where Homebrew, nvm or a language manager
// sets itself up there, the result has none of them — so a service installed
// from a `zsh -lc` lookup would run a shell with no node, no gh and no java.
// The cost is that .zshrc gets sourced on top of an already populated PATH,
// which is why the result is deduplicated rather than used verbatim.
//
// It also returns how the shell was chosen, so the report can name it instead of
// implying a shell was consulted that was never asked.
func loginShellPath() (string, string, error) {
	shell, how := loginShell()
	out, err := exec.Command(shell, "-lic", "echo $PATH").Output()
	if err != nil {
		return "", how, fmt.Errorf("run %s -lic: %w", shell, err)
	}
	return strings.TrimSpace(string(out)), how, nil
}

// loginShell finds the shell to ask.
//
// $SHELL is the usual answer, but the contexts where capturing a PATH matters
// most — an IDE, a cron job, launchd itself, a CI step — are exactly the ones
// that do not set it. Falling back to /bin/sh there is not a small difference:
// it yields a PATH with no Homebrew, no version managers and none of the toolchain
// the agent exists to drive, and it does so silently, because /bin/sh -lic exits
// zero. So the account's own login shell is read from the directory services
// database when $SHELL is missing.
func loginShell() (shell, how string) {
	if s := os.Getenv("SHELL"); s != "" {
		return s, "read from $SHELL"
	}
	if s := accountLoginShell(); s != "" {
		return s, "read from the account's login shell, because $SHELL was not set"
	}
	return "/bin/sh", "assumed /bin/sh: $SHELL was not set and the account has no login shell on record"
}

// accountLoginShell reads the login shell recorded for the current user.
//
// macOS keeps /etc/passwd nearly empty and answers from Open Directory, so
// dscl is the portable answer there and getent is the answer everywhere else.
// Both are part of a base install on their platform; a failure just means the
// caller falls back, so the error is not worth surfacing on its own.
func accountLoginShell() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
		if err != nil {
			return ""
		}
		return lastField(strings.TrimSpace(string(out)))
	}
	out, err := exec.Command("getent", "passwd", u.Username).Output()
	if err != nil {
		return ""
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) < 7 {
		return ""
	}
	return strings.TrimSpace(fields[6])
}

// lastField returns the value after "Key: " in a dscl answer, or the last
// whitespace-separated token if there is no such separator.
func lastField(out string) string {
	if i := strings.LastIndex(out, ": "); i >= 0 {
		return strings.TrimSpace(out[i+2:])
	}
	return lastFieldFallback(out)
}

func lastFieldFallback(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// describeService prints where a service would be installed, so the paths can
// be checked before anything is written.
func describeService(svc *servicedef.Service, paths servicedef.Paths) error {
	fmt.Printf("Label:       %s\n", svc.Label)
	fmt.Printf("Executable:  %s\n", svc.Executable)
	fmt.Printf("Working dir: %s\n", svc.WorkingDir)
	fmt.Printf("Definition:  %s\n", paths.Plist)
	fmt.Printf("Stdout log:  %s\n", paths.LogStdout)
	fmt.Printf("Stderr log:  %s\n", paths.LogStderr)
	fmt.Printf("State file:  %s\n", svc.StateFile)
	return nil
}

// confirmService shows the generated definition and asks before installing.
// The definition is printed rather than merely described, because the generated
// file is the only place where a wrong PATH or a stray environment entry is
// actually visible.
func confirmService(svc *servicedef.Service, paths servicedef.Paths) (bool, error) {
	if err := describeService(svc, paths); err != nil {
		return false, err
	}
	fmt.Println()
	def, err := renderService(svc, paths)
	if err != nil {
		return false, err
	}
	fmt.Println("Generated definition:")
	fmt.Println(string(def))
	return confirm()
}

// confirm asks a yes/no question, defaulting to no. Anything other than an
// explicit yes is a refusal, so a stray newline cannot install something.
func confirm() (bool, error) {
	fmt.Print("Continue? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// describeResolvedPaths prints where the agent will read and write, so the
// paths a service job gets can be compared against what the running agent uses.
func describeResolvedPaths(cfg *config.Config) {
	path, enabled := audit.PathFor(cfg.Security.AuditLog)
	switch {
	case !enabled:
		fmt.Println("Audit log:      off")
	case path == "":
		fmt.Println("Audit log:      (unresolved: no home directory; set security.audit_log)")
	default:
		fmt.Printf("Audit log:      %s\n", path)
	}
	if state, err := session.ResolveStateFile(cfg.StateFile); err == nil {
		fmt.Printf("State file:     %s\n", state)
	} else {
		fmt.Printf("State file:     (unresolved: %v)\n", err)
	}
}
