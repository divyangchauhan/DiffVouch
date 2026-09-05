package review

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/dv"
	"github.com/divyangchauhan/DiffVouch/internal/gitdiff"
	"github.com/divyangchauhan/DiffVouch/internal/github"
	"github.com/divyangchauhan/DiffVouch/internal/model"
	"github.com/divyangchauhan/DiffVouch/internal/provider"
	"github.com/divyangchauhan/DiffVouch/internal/rating"
	"github.com/divyangchauhan/DiffVouch/internal/sanitize"
)

type Options struct {
	ProviderName         string
	Transport            string
	Model                string
	Effort               string
	Base                 string
	CommittedOnly        bool
	StagedOnly           bool
	Excludes             []string
	ConfigPath           string
	MaxDiffBytes         int
	FailBelow            *float64
	FailOnSeverity       string
	PublicationRequested bool
	ReviewPrompt         string
	Root                 string
}

func Perform(options Options) (*model.ReviewResult, *model.FilesSummary, error) {
	repo, err := gitdiff.RepositoryRoot(options.Root)
	if err != nil {
		return nil, nil, err
	}
	trustedRef := "HEAD"
	if options.Base != "" {
		resolved, resolveErr := gitdiff.Run(repo, 4096, "rev-parse", "--verify", options.Base+"^{commit}")
		if resolveErr != nil {
			return nil, nil, resolveErr
		}
		trustedRef = strings.TrimSpace(resolved)
	}
	repositoryConfig, err := config.LoadRepository(repo, options.ConfigPath, trustedRef)
	if err != nil {
		return nil, nil, dv.Wrap(dv.ExitArguments, "load repository configuration", err)
	}
	transport := options.Transport
	if transport == "" {
		transport = "cli"
	}
	selectedModel := options.Model
	if selectedModel == "" {
		if options.ProviderName == "codex" {
			selectedModel = repositoryConfig.Provider.Models.Codex
		} else {
			selectedModel = repositoryConfig.Provider.Models.Claude
		}
	}
	maxBytes := options.MaxDiffBytes
	if maxBytes == 0 {
		maxBytes = repositoryConfig.Review.MaxDiffBytes
	}
	collected, err := gitdiff.Collect(gitdiff.Options{
		Root: repo, Base: options.Base, CommittedOnly: options.CommittedOnly,
		StagedOnly:   options.StagedOnly,
		Excludes:     append(append([]string{}, repositoryConfig.Review.Exclude...), options.Excludes...),
		MaxDiffBytes: maxBytes,
	})
	if err != nil {
		return nil, nil, err
	}
	if collected.Patch == "" {
		files := &model.FilesSummary{Reviewed: collected.ReviewedFiles, Excluded: collected.ExcludedFiles, Binary: collected.BinaryFiles, Omitted: []string{}}
		return nil, files, nil
	}
	redacted, redactions := sanitize.Redact(collected.Patch)
	chunks, err := gitdiff.ChunkPatch(redacted, repositoryConfig.Review.ChunkBytes)
	if err != nil {
		return nil, nil, err
	}
	adapter, err := provider.New(provider.Options{Name: options.ProviderName, Transport: transport, Model: selectedModel, Effort: options.Effort})
	if err != nil {
		return nil, nil, err
	}
	reviews := make([]model.ProviderReview, 0, len(chunks))
	sizes := make([]int, 0, len(chunks))
	for index, chunk := range chunks {
		prompt := buildPrompt(chunk, index+1, len(chunks), repositoryConfig.Review.Rubric, repositoryConfig.Review.Instructions, options.ReviewPrompt)
		providerReview, reviewErr := adapter.Review(prompt)
		if reviewErr != nil {
			return nil, nil, reviewErr
		}
		reviews = append(reviews, providerReview)
		sizes = append(sizes, len([]byte(chunk)))
	}
	combined := combine(reviews, sizes)
	reviewedPaths := map[string]struct{}{}
	for _, path := range collected.ReviewedFiles {
		reviewedPaths[path] = struct{}{}
	}
	accepted, rejectedLocations := validateFindingLocations(combined.Findings, reviewedPaths, github.ChangedLines(collected.Patch))
	combined.NeedsVerification = append(combined.NeedsVerification, rejectedLocations...)
	combined.Findings = rating.NormalizeFindings(accepted)
	if combined.Findings == nil {
		combined.Findings = []model.Finding{}
	}
	if combined.PositiveObservations == nil {
		combined.PositiveObservations = []string{}
	}
	if combined.NeedsVerification == nil {
		combined.NeedsVerification = []string{}
	}
	calculatedRating := rating.Calculate(combined, repositoryConfig.Review.Rubric)
	failBelow := options.FailBelow
	if failBelow == nil {
		failBelow = repositoryConfig.QualityGate.FailBelow
	}
	failSeverity := options.FailOnSeverity
	if failSeverity == "" {
		failSeverity = repositoryConfig.QualityGate.FailOnSeverity
	}
	if failSeverity != "" && !rating.ValidSeverity(failSeverity) {
		return nil, nil, dv.New(dv.ExitArguments, "invalid fail-on severity")
	}
	var effort *string
	if options.Effort != "" {
		effort = &options.Effort
	}
	result := &model.ReviewResult{
		SchemaVersion: 1, ReviewID: model.NewReviewID(), Status: "complete", Partial: false,
		Scope:    model.Scope{Mode: collected.Mode, BaseRef: collected.BaseRef, BaseSHA: collected.BaseSHA, MergeBase: collected.MergeBase, HeadSHA: collected.HeadSHA},
		Provider: model.ProviderInfo{Name: options.ProviderName, Transport: transport, Model: adapter.Model(), Effort: effort},
		Rating:   calculatedRating, Summary: combined.Summary, Findings: combined.Findings,
		PositiveObservations: combined.PositiveObservations, NeedsVerification: combined.NeedsVerification,
		Files:       model.FilesSummary{Reviewed: collected.ReviewedFiles, Excluded: collected.ExcludedFiles, Binary: collected.BinaryFiles, Omitted: []string{}, Redactions: redactions},
		Gate:        rating.Gate(calculatedRating, combined.Findings, failBelow, failSeverity),
		Publication: model.Publication{Requested: options.PublicationRequested},
	}
	return result, nil, nil
}

func validateFindingLocations(findings []model.Finding, reviewedPaths map[string]struct{}, changedLines map[github.DiffLocation]struct{}) ([]model.Finding, []string) {
	accepted := make([]model.Finding, 0, len(findings))
	var needsVerification []string
	for _, finding := range findings {
		if finding.Path != nil {
			if _, ok := reviewedPaths[*finding.Path]; !ok {
				needsVerification = append(needsVerification, fmt.Sprintf("Provider cited %q, which was not part of the reviewed patch.", *finding.Path))
				continue
			}
		}
		if finding.Line != nil && finding.Path != nil && finding.Side != nil {
			side := "RIGHT"
			if *finding.Side == "old" {
				side = "LEFT"
			}
			if _, ok := changedLines[github.DiffLocation{Path: *finding.Path, Side: side, Line: *finding.Line}]; !ok {
				needsVerification = append(needsVerification, fmt.Sprintf("Provider cited %s:%d on an unchanged or unavailable line.", *finding.Path, *finding.Line))
				continue
			}
		}
		accepted = append(accepted, finding)
	}
	return accepted, needsVerification
}

func combine(reviews []model.ProviderReview, sizes []int) model.ProviderReview {
	if len(reviews) == 1 {
		return reviews[0]
	}
	total := 0
	for _, size := range sizes {
		total += size
	}
	combined := model.ProviderReview{}
	var correctness, security, maintainability, testing, scope float64
	seenFindings := map[string]struct{}{}
	seenPositive := map[string]struct{}{}
	seenVerification := map[string]struct{}{}
	var summaries []string
	for index, review := range reviews {
		weight := float64(sizes[index]) / float64(total)
		correctness += review.Dimensions.Correctness * weight
		security += review.Dimensions.Security * weight
		maintainability += review.Dimensions.Maintainability * weight
		testing += review.Dimensions.Testing * weight
		scope += review.Dimensions.Scope * weight
		summaries = append(summaries, strings.TrimSpace(review.Summary))
		for _, finding := range review.Findings {
			path := ""
			line := 0
			if finding.Path != nil {
				path = *finding.Path
			}
			if finding.Line != nil {
				line = *finding.Line
			}
			key := fmt.Sprintf("%s:%d:%s", path, line, strings.ToLower(finding.Title))
			if _, ok := seenFindings[key]; !ok {
				seenFindings[key] = struct{}{}
				combined.Findings = append(combined.Findings, finding)
			}
		}
		for _, value := range review.PositiveObservations {
			if _, ok := seenPositive[value]; !ok {
				seenPositive[value] = struct{}{}
				combined.PositiveObservations = append(combined.PositiveObservations, value)
			}
		}
		for _, value := range review.NeedsVerification {
			if _, ok := seenVerification[value]; !ok {
				seenVerification[value] = struct{}{}
				combined.NeedsVerification = append(combined.NeedsVerification, value)
			}
		}
	}
	round := func(value float64) float64 { return math.Round(value*100) / 100 }
	combined.Dimensions = model.Dimensions{Correctness: round(correctness), Security: round(security), Maintainability: round(maintainability), Testing: round(testing), Scope: round(scope)}
	combined.Summary = fmt.Sprintf("Reviewed the complete patch in %d chunks. %s", len(reviews), strings.Join(summaries, " "))
	sort.Strings(combined.PositiveObservations)
	sort.Strings(combined.NeedsVerification)
	return combined
}
