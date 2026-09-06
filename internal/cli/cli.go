package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/dv"
	"github.com/divyangchauhan/DiffVouch/internal/gitdiff"
	"github.com/divyangchauhan/DiffVouch/internal/github"
	"github.com/divyangchauhan/DiffVouch/internal/model"
	"github.com/divyangchauhan/DiffVouch/internal/render"
	"github.com/divyangchauhan/DiffVouch/internal/review"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func Execute(version string) error {
	root := &cobra.Command{
		Use: "diffvouch", Short: "Local-first AI code reviews",
		Version: version, SilenceErrors: true, SilenceUsage: true,
	}
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.AddCommand(reviewCommand(), authCommand(), githubCommand())
	return root.Execute()
}

type reviewFlags struct {
	provider, transport, model, effort, base, format, output, repo, githubHost string
	configPath, failSeverity                                                   string
	pr                                                                         int
	committedOnly, stagedOnly, publish, noColor, verbose                       bool
	excludes                                                                   []string
	failBelow                                                                  float64
	maxDiffBytes                                                               int
}

func reviewCommand() *cobra.Command {
	flags := &reviewFlags{}
	command := &cobra.Command{
		Use: "review", Short: "Review a Git diff or pull request",
		RunE: func(command *cobra.Command, _ []string) error { return runReview(command, flags) },
	}
	command.Flags().StringVar(&flags.provider, "provider", "", "provider: codex or claude (required)")
	command.Flags().StringVar(&flags.transport, "transport", "", "transport: cli or api (default cli)")
	command.Flags().StringVar(&flags.model, "model", "", "provider model override")
	command.Flags().StringVar(&flags.effort, "effort", "", "reasoning effort: low, medium, high, xhigh, max, or ultra")
	command.Flags().StringVar(&flags.base, "base", "", "local Git ref to compare against")
	command.Flags().IntVar(&flags.pr, "pr", 0, "GitHub pull request number")
	command.Flags().StringVar(&flags.repo, "repo", "", "GitHub owner/name override")
	command.Flags().StringVar(&flags.githubHost, "github-host", "", "GitHub hostname override")
	command.Flags().BoolVar(&flags.committedOnly, "committed-only", false, "exclude working-tree changes")
	command.Flags().BoolVar(&flags.stagedOnly, "staged-only", false, "review staged changes only")
	command.Flags().StringArrayVar(&flags.excludes, "exclude", nil, "exclude path glob (repeatable)")
	command.Flags().StringVar(&flags.format, "format", "terminal", "output format: terminal or json")
	command.Flags().StringVar(&flags.output, "output", "", "also write output to this file")
	command.Flags().BoolVar(&flags.publish, "publish", false, "publish a COMMENT review through the configured GitHub App")
	command.Flags().Float64Var(&flags.failBelow, "fail-below", 0, "fail when rating is below this value")
	command.Flags().StringVar(&flags.failSeverity, "fail-on-severity", "", "fail at or above this severity")
	command.Flags().IntVar(&flags.maxDiffBytes, "max-diff-bytes", 0, "override diff safety limit")
	command.Flags().StringVar(&flags.configPath, "config", "", "explicit repository config path")
	command.Flags().BoolVar(&flags.noColor, "no-color", false, "disable color output")
	command.Flags().BoolVar(&flags.verbose, "verbose", false, "enable verbose diagnostics")
	_ = command.MarkFlagRequired("provider")
	return command
}

func runReview(command *cobra.Command, flags *reviewFlags) error {
	if flags.provider != "codex" && flags.provider != "claude" {
		return dv.New(dv.ExitArguments, "--provider must be codex or claude")
	}
	if flags.transport != "" && flags.transport != "cli" && flags.transport != "api" {
		return dv.New(dv.ExitArguments, "--transport must be cli or api")
	}
	if flags.format != "terminal" && flags.format != "json" {
		return dv.New(dv.ExitArguments, "--format must be terminal or json")
	}
	if flags.effort != "" && !contains([]string{"low", "medium", "high", "xhigh", "max", "ultra"}, flags.effort) {
		return dv.New(dv.ExitArguments, "invalid --effort")
	}
	if flags.pr > 0 && flags.stagedOnly {
		return dv.New(dv.ExitArguments, "--pr cannot be combined with --staged-only")
	}
	if flags.publish && flags.stagedOnly {
		return dv.New(dv.ExitArguments, "--publish cannot be combined with --staged-only")
	}
	if flags.maxDiffBytes != 0 && flags.maxDiffBytes < 10_000 {
		return dv.New(dv.ExitArguments, "--max-diff-bytes must be at least 10000")
	}
	var failBelow *float64
	if command.Flags().Changed("fail-below") {
		if flags.failBelow < 1 || flags.failBelow > 5 {
			return dv.New(dv.ExitArguments, "--fail-below must be between 1 and 5")
		}
		failBelow = &flags.failBelow
	}
	repoRoot, err := gitdiff.RepositoryRoot("")
	if err != nil {
		return err
	}
	base := flags.base
	resolvedRepo := flags.repo
	if flags.pr > 0 {
		name, pull, resolveErr := github.ResolvePull(repoRoot, flags.repo, flags.githubHost, flags.pr)
		if resolveErr != nil {
			return resolveErr
		}
		resolvedRepo = name
		if pull.State != "open" {
			return dv.New(dv.ExitGitHub, "pull request is not open")
		}
		localHead, headErr := gitdiff.Run(repoRoot, 4096, "rev-parse", "HEAD")
		if headErr != nil {
			return headErr
		}
		if strings.TrimSpace(localHead) != pull.Head.SHA {
			return dv.New(dv.ExitGitHub, "local HEAD does not match the pull request head")
		}
		if base != "" {
			baseSHA, baseErr := gitdiff.Run(repoRoot, 4096, "rev-parse", "--verify", base+"^{commit}")
			if baseErr != nil {
				return baseErr
			}
			if strings.TrimSpace(baseSHA) != pull.Base.SHA {
				return dv.New(dv.ExitGitHub, "--base does not resolve to the pull request base")
			}
		}
		if _, baseErr := gitdiff.Run(repoRoot, 4096, "cat-file", "-e", pull.Base.SHA+"^{commit}"); baseErr != nil {
			return dv.New(dv.ExitGit, "pull request base commit is not available locally; fetch it explicitly")
		}
		base = pull.Base.SHA
	}
	if flags.publish && base == "" && flags.pr == 0 {
		return dv.New(dv.ExitArguments, "--publish requires --pr or --base")
	}
	result, skipped, err := review.Perform(review.Options{
		ProviderName: flags.provider, Transport: flags.transport, Model: flags.model, Effort: flags.effort,
		Base: base, CommittedOnly: flags.committedOnly || flags.pr > 0 || flags.publish,
		StagedOnly: flags.stagedOnly, Excludes: flags.excludes, ConfigPath: flags.configPath,
		MaxDiffBytes: flags.maxDiffBytes, FailBelow: failBelow, FailOnSeverity: flags.failSeverity,
		PublicationRequested: flags.publish, Root: repoRoot, Diagnostics: command.ErrOrStderr(),
	})
	if err != nil {
		return err
	}
	if result == nil {
		if skipped != nil && (len(skipped.Binary) > 0 || len(skipped.Excluded) > 0) {
			_, _ = fmt.Fprintln(command.OutOrStdout(), "No reviewable text changes.")
			if len(skipped.Binary) > 0 {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "Skipped binary or non-UTF-8 files: %s\n", strings.Join(displayPaths(skipped.Binary), ", "))
			}
			if len(skipped.Excluded) > 0 {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "Excluded files: %s\n", strings.Join(displayPaths(skipped.Excluded), ", "))
			}
		} else {
			_, _ = fmt.Fprintln(command.OutOrStdout(), "No reviewable changes.")
		}
		return nil
	}
	if flags.publish {
		url, bot, publishErr := github.Publish(result, repoRoot, resolvedRepo, flags.githubHost, flags.pr)
		if publishErr != nil {
			_ = writeReviewOutput(command, *result, flags)
			return publishErr
		}
		result.Publication.Published, result.Publication.URL, result.Publication.Bot = true, &url, &bot
	}
	if err := writeReviewOutput(command, *result, flags); err != nil {
		return err
	}
	if !result.Gate.Passed {
		return dv.New(dv.ExitGate, "quality gate failed")
	}
	return nil
}

func writeReviewOutput(command *cobra.Command, result model.ReviewResult, flags *reviewFlags) error {
	var value string
	var err error
	if flags.format == "json" {
		value, err = render.JSON(result)
	} else {
		value = render.Terminal(result)
	}
	if err != nil {
		return dv.Wrap(dv.ExitArguments, "render review output", err)
	}
	if _, err := io.WriteString(command.OutOrStdout(), value); err != nil {
		return dv.Wrap(dv.ExitArguments, "write review output", err)
	}
	if flags.output != "" {
		if err := os.WriteFile(expandHome(flags.output), []byte(value), 0o600); err != nil {
			return dv.Wrap(dv.ExitArguments, "write review output file", err)
		}
	}
	return nil
}

func authCommand() *cobra.Command {
	command := &cobra.Command{Use: "auth", Short: "Manage AI provider authentication"}
	command.AddCommand(authLoginCommand(), authStatusCommand(), authSetKeyCommand(), authRemoveKeyCommand())
	return command
}

func authLoginCommand() *cobra.Command {
	var device bool
	command := &cobra.Command{Use: "login <openai|codex|claude>", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		var name string
		var commandArgs []string
		if args[0] == "openai" || args[0] == "codex" {
			name = "codex"
			commandArgs = []string{"login"}
			if device {
				commandArgs = append(commandArgs, "--device-auth")
			}
		} else if args[0] == "claude" {
			if device {
				return dv.New(dv.ExitArguments, "--device is supported only for Codex")
			}
			name = "claude"
			commandArgs = []string{"auth", "login"}
		} else {
			return dv.New(dv.ExitArguments, "unknown provider")
		}
		child := exec.Command(name, commandArgs...)
		child.Stdin, child.Stdout, child.Stderr = command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr()
		if err := child.Run(); err != nil {
			return dv.Wrap(dv.ExitProvider, name+" login failed", err)
		}
		return nil
	}}
	command.Flags().BoolVar(&device, "device", false, "use Codex device-code login")
	return command
}

func authStatusCommand() *cobra.Command {
	return &cobra.Command{Use: "status [openai|codex|claude|all]", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		requested := "all"
		if len(args) == 1 {
			requested = args[0]
		}
		if !contains([]string{"openai", "codex", "claude", "all"}, requested) {
			return dv.New(dv.ExitArguments, "unknown provider")
		}
		providers := []string{"openai", "claude"}
		if requested != "all" {
			providers = []string{requested}
			if requested == "codex" {
				providers[0] = "openai"
			}
		}
		global, err := config.LoadGlobal()
		if err != nil {
			return err
		}
		failed := false
		for _, name := range providers {
			cliName, apiName, label, statusArgs := "codex", "openai", "OpenAI (Codex subscription)", []string{"login", "status"}
			if name == "claude" {
				cliName, apiName, label, statusArgs = "claude", "anthropic", "Claude subscription", []string{"auth", "status", "--text"}
			}
			status := exec.Command(cliName, statusArgs...)
			raw, statusErr := status.CombinedOutput()
			ready := statusErr == nil
			detail := strings.TrimSpace(string(raw))
			if detail != "" {
				detail = " — " + detail
			}
			fmt.Fprintf(command.OutOrStdout(), "%s: %s%s\n", label, ternary(ready, "ready", "not ready"), detail)
			_, apiReady := global.APIKeys[apiName]
			fmt.Fprintf(command.OutOrStdout(), "%s API key: %s\n", apiName, ternary(apiReady, "configured", "not configured"))
			failed = failed || !(ready || apiReady)
		}
		if failed {
			return dv.New(dv.ExitGate, "no configured authentication method for one or more providers")
		}
		return nil
	}}
}

func authSetKeyCommand() *cobra.Command {
	var fromStdin bool
	var storage string
	command := &cobra.Command{Use: "set-key <openai|anthropic>", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		providerName := args[0]
		if providerName != "openai" && providerName != "anthropic" {
			return dv.New(dv.ExitArguments, "provider must be openai or anthropic")
		}
		var raw []byte
		var err error
		if fromStdin {
			raw, err = io.ReadAll(io.LimitReader(command.InOrStdin(), 64*1024))
		} else {
			fmt.Fprintf(command.ErrOrStderr(), "%s API key: ", providerName)
			raw, err = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(command.ErrOrStderr())
		}
		if err != nil {
			return dv.Wrap(dv.ExitArguments, "read API key", err)
		}
		value := strings.TrimSpace(string(raw))
		global, err := config.LoadGlobal()
		if err != nil {
			return err
		}
		previous, hadPrevious := global.APIKeys[providerName]
		ref, err := secret.Store(fmt.Sprintf("api-key:%s:%d", providerName, time.Now().UnixNano()), value, storage)
		if err != nil {
			return err
		}
		global.APIKeys[providerName] = ref
		if err := config.SaveGlobal(global); err != nil {
			_ = secret.Delete(ref)
			return err
		}
		if hadPrevious && previous != ref {
			_ = secret.Delete(previous)
		}
		fmt.Fprintf(command.OutOrStdout(), "Stored %s API key in %s storage.\n", providerName, ref.Backend)
		return nil
	}}
	command.Flags().BoolVar(&fromStdin, "stdin", false, "read key from stdin")
	command.Flags().StringVar(&storage, "storage", "auto", "secret storage: auto, keyring, or file")
	return command
}

func authRemoveKeyCommand() *cobra.Command {
	return &cobra.Command{Use: "remove-key <openai|anthropic>", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		global, err := config.LoadGlobal()
		if err != nil {
			return err
		}
		ref, ok := global.APIKeys[args[0]]
		if !ok {
			fmt.Fprintf(command.OutOrStdout(), "No stored %s API key.\n", args[0])
			return nil
		}
		delete(global.APIKeys, args[0])
		if err := config.SaveGlobal(global); err != nil {
			return err
		}
		_ = secret.Delete(ref)
		fmt.Fprintf(command.OutOrStdout(), "Removed %s API key.\n", args[0])
		return nil
	}}
}

func githubCommand() *cobra.Command {
	root := &cobra.Command{Use: "github", Short: "Manage GitHub review publication"}
	app := &cobra.Command{Use: "app", Short: "Manage a user-owned GitHub App"}
	app.AddCommand(githubCreateCommand(), githubConfigureCommand(), githubStatusCommand(), githubRemoveCommand())
	root.AddCommand(app)
	return root
}

func githubCreateCommand() *cobra.Command {
	var name, host, owner, storage, apiBase, webBase, apiVersion, code string
	var noBrowser bool
	var timeout time.Duration
	command := &cobra.Command{Use: "create", RunE: func(command *cobra.Command, _ []string) error {
		options := github.ManifestCreateOptions{
			Name: name, Owner: owner, Host: host, Storage: storage,
			APIBaseURL: apiBase, WebBaseURL: webBase, APIVersion: apiVersion,
			NoBrowser: noBrowser, Timeout: timeout,
			OnReady: func(startURL string, browserErr error) {
				fmt.Fprintf(command.OutOrStdout(), "Creating a private GitHub App with Pull requests: Read and write, no events, and disabled webhooks.\nComplete GitHub's confirmation at:\n  %s\n", startURL)
				if browserErr != nil && !noBrowser {
					fmt.Fprintf(command.ErrOrStderr(), "Could not open a browser automatically: %v\n", browserErr)
				}
			},
		}
		var created github.ManifestCreation
		var err error
		if code != "" {
			created, err = github.CreateFromManifestCode(options, code)
		} else {
			created, err = github.CreateFromManifest(command.Context(), options)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "Created and securely configured %s[bot] (App ID %s).\nInstall it on selected repositories:\n  %s\n", created.App.Slug, created.App.AppID, created.InstallURL)
		if !noBrowser {
			if err := github.OpenBrowser(created.InstallURL); err != nil {
				fmt.Fprintf(command.ErrOrStderr(), "Could not open the installation page automatically: %v\n", err)
			}
		}
		return nil
	}}
	command.Flags().StringVar(&name, "name", "", "proposed app name (default: randomized DiffVouch name)")
	command.Flags().StringVar(&host, "host", "github.com", "GitHub hostname")
	command.Flags().StringVar(&owner, "owner", "", "organization that should own the app")
	command.Flags().StringVar(&storage, "storage", "auto", "secret storage: auto, keyring, or file")
	command.Flags().StringVar(&apiBase, "api-base-url", "", "REST API base URL")
	command.Flags().StringVar(&webBase, "web-base-url", "", "web base URL")
	command.Flags().StringVar(&apiVersion, "api-version", "", "GitHub REST API version")
	command.Flags().StringVar(&code, "code", "", "exchange a manifest code when localhost redirect was unavailable")
	command.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "maximum time to wait for GitHub confirmation")
	command.Flags().BoolVar(&noBrowser, "no-browser", false, "print URLs instead of opening a browser")
	return command
}

func githubConfigureCommand() *cobra.Command {
	var appID, slug, keyPath, host, apiBase, webBase, apiVersion, storage string
	command := &cobra.Command{Use: "configure", RunE: func(command *cobra.Command, _ []string) error {
		reader := bufio.NewReader(command.InOrStdin())
		if appID == "" {
			appID = prompt(reader, command, "GitHub App ID")
		}
		if slug == "" {
			slug = prompt(reader, command, "GitHub App slug")
		}
		if keyPath == "" {
			keyPath = prompt(reader, command, "Downloaded private-key PEM path")
		}
		configured, err := github.Configure(appID, slug, expandHome(keyPath), host, storage, apiBase, webBase, apiVersion)
		if err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "Configured and validated %s[bot] for %s.\nInstall or update repository access: %s/apps/%s/installations/new\n", configured.Slug, configured.Host, configured.WebBaseURL, configured.Slug)
		return nil
	}}
	command.Flags().StringVar(&appID, "app-id", "", "GitHub App ID")
	command.Flags().StringVar(&slug, "slug", "", "GitHub App slug")
	command.Flags().StringVar(&keyPath, "private-key", "", "downloaded private-key PEM path")
	command.Flags().StringVar(&host, "host", "github.com", "GitHub hostname")
	command.Flags().StringVar(&apiBase, "api-base-url", "", "REST API base URL")
	command.Flags().StringVar(&webBase, "web-base-url", "", "web base URL")
	command.Flags().StringVar(&apiVersion, "api-version", "", "GitHub REST API version")
	command.Flags().StringVar(&storage, "storage", "auto", "secret storage: auto, keyring, or file")
	return command
}

func githubStatusCommand() *cobra.Command {
	var host, repo string
	command := &cobra.Command{Use: "status", RunE: func(command *cobra.Command, _ []string) error {
		app, err := github.LoadApp(host)
		if err != nil {
			return err
		}
		client, err := github.NewClient(app)
		if err != nil {
			return err
		}
		details, err := client.App()
		if err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "App: %s[bot] (ID %v)\nHost: %s\nPrivate key storage: %s\n", app.Slug, details["id"], app.Host, app.PrivateKey.Backend)
		if repo != "" {
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 {
				return dv.New(dv.ExitArguments, "--repo must be owner/name")
			}
			if _, err := client.InstallationToken(parts[0], parts[1]); err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Installation: ready for %s\nInstallation token: generated in memory and discarded\n", repo)
		}
		return nil
	}}
	command.Flags().StringVar(&host, "host", "github.com", "GitHub hostname")
	command.Flags().StringVar(&repo, "repo", "", "owner/name installation to validate")
	return command
}

func githubRemoveCommand() *cobra.Command {
	var host string
	command := &cobra.Command{Use: "remove", RunE: func(command *cobra.Command, _ []string) error {
		removed, err := github.RemoveApp(host)
		if err != nil {
			return err
		}
		if removed {
			fmt.Fprintf(command.OutOrStdout(), "Removed local GitHub App credentials for %s. Revoke the key in GitHub if it is no longer used.\n", host)
		} else {
			fmt.Fprintf(command.OutOrStdout(), "No GitHub App was configured for %s.\n", host)
		}
		return nil
	}}
	command.Flags().StringVar(&host, "host", "github.com", "GitHub hostname")
	return command
}

func prompt(reader *bufio.Reader, command *cobra.Command, label string) string {
	fmt.Fprintf(command.ErrOrStderr(), "%s: ", label)
	value, _ := reader.ReadString('\n')
	return strings.TrimSpace(value)
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func displayPaths(paths []string) []string {
	display := make([]string, len(paths))
	for index, path := range paths {
		display[index] = gitdiff.DisplayPath(path)
	}
	return display
}
func ternary(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
