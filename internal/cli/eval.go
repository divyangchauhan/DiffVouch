package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/eval"
	"github.com/spf13/cobra"
)

func evalCommand() *cobra.Command {
	var dir string
	root := &cobra.Command{Use: "eval", Short: "Prepare, run, and report frozen review evaluations"}
	root.PersistentFlags().StringVar(&dir, "dir", ".diffvouch-evals", "evaluation corpus and checkpoint directory")
	prepare := &cobra.Command{Use: "prepare", Short: "Freeze both public benchmarks and an owner's public PRs"}
	var owner string
	prepare.Flags().StringVar(&owner, "owner", "divyangchauhan", "public GitHub repository owner")
	prepare.RunE = func(cmd *cobra.Command, _ []string) error {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		return eval.Prepare(ctx, abs, owner, cmd.OutOrStdout())
	}
	o := eval.RunOptions{}
	var cutoff string
	run := &cobra.Command{Use: "run", Short: "Run native subscription reviews and common judgments; resume checkpoints"}
	run.Flags().StringVar(&o.Model, "model", "gpt-5.6-sol", "measured reviewer model")
	run.Flags().StringVar(&o.Effort, "effort", "high", "review reasoning effort")
	run.Flags().StringVar(&o.Judge, "judge", "gpt-5.6-sol", "independent judge model")
	run.Flags().StringVar(&o.Astra, "adjudicator", "gpt-6-astra", "hard-case adjudicator; empty disables escalation")
	run.Flags().StringVar(&o.Cohort, "cohort", "evaluation", "evaluation or development; development never enters held-out scores")
	run.Flags().StringVar(&o.Suite, "suite", "", "optional suite: martian, swe-prbench, personal")
	run.Flags().StringVar(&o.Phase, "phase", "all", "all, review, or grade; grade requires saved reviews")
	run.Flags().StringVar(&o.Tools, "tools", "all", "archived tools: all, none, or comma-separated names")
	run.Flags().IntVar(&o.Limit, "limit", 0, "maximum cases to visit; zero selects the full corpus")
	run.Flags().IntVar(&o.Repeat, "repeat", 0, "independent repeat number on a fixed 20 percent subset")
	run.Flags().BoolVar(&o.RetryErrors, "retry-errors", false, "retry recorded failures while retaining saved reviews")
	run.Flags().StringVar(&cutoff, "use-reset-expiring-before", "", "authorize an earned reset expiring before this RFC3339 time, only on exhaustion")
	run.RunE = func(cmd *cobra.Command, _ []string) error {
		if o.Cohort != "evaluation" && o.Cohort != "development" {
			return fmt.Errorf("invalid cohort")
		}
		if o.Limit < 0 || o.Repeat < 0 {
			return fmt.Errorf("limit and repeat cannot be negative")
		}
		if o.Phase != "all" && o.Phase != "review" && o.Phase != "grade" {
			return fmt.Errorf("invalid phase")
		}
		if o.Suite != "" && o.Suite != "martian" && o.Suite != "swe-prbench" && o.Suite != "personal" {
			return fmt.Errorf("invalid suite")
		}
		if !contains([]string{"low", "medium", "high", "xhigh", "max", "ultra"}, o.Effort) {
			return fmt.Errorf("invalid effort")
		}
		if o.Model == "" || o.Judge == "" {
			return fmt.Errorf("reviewer and judge models are required")
		}
		var err error
		o.Directory, err = filepath.Abs(dir)
		if err != nil {
			return err
		}
		if cutoff != "" {
			o.ResetBefore, err = time.Parse(time.RFC3339, cutoff)
			if err != nil {
				return err
			}
		}
		o.Output = cmd.OutOrStdout()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		return eval.Run(ctx, o)
	}
	var runDir string
	report := &cobra.Command{Use: "report", Short: "Regenerate coverage and scores without model calls", RunE: func(cmd *cobra.Command, _ []string) error {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		return eval.Report(abs, runDir)
	}}
	report.Flags().StringVar(&runDir, "run-dir", "", "specific run directory; defaults to latest")
	var before, after string
	compare := &cobra.Command{Use: "compare", Short: "Compare two runs on shared cases without a regression gate", RunE: func(cmd *cobra.Command, _ []string) error {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		return eval.Compare(abs, before, after)
	}}
	compare.Flags().StringVar(&before, "before", "", "earlier run directory")
	compare.Flags().StringVar(&after, "after", "", "later run directory")
	_ = compare.MarkFlagRequired("before")
	_ = compare.MarkFlagRequired("after")
	root.AddCommand(prepare, run, report, compare)
	return root
}
