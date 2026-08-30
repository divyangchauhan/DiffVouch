package rating

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/divyangchauhan/DiffVouch/internal/model"
)

func Calculate(review model.ProviderReview, weights map[string]int) model.Rating {
	dimensions := review.Dimensions
	value := dimensions.Correctness*float64(weights["correctness"])/100 +
		dimensions.Security*float64(weights["security"])/100 +
		dimensions.Maintainability*float64(weights["maintainability"])/100 +
		dimensions.Testing*float64(weights["testing"])/100 +
		dimensions.Scope*float64(weights["scope"])/100
	capValue := 5.0
	for _, finding := range review.Findings {
		switch finding.Severity {
		case model.Critical:
			capValue = math.Min(capValue, 1.5)
		case model.High:
			capValue = math.Min(capValue, 2.5)
		}
	}
	overall := math.Round(math.Min(value, capValue)*10) / 10
	label := "Excellent"
	switch {
	case overall < 1.5:
		label = "Critical risk"
	case overall < 2:
		label = "High risk"
	case overall < 3.5:
		label = "Needs work"
	case overall < 4.5:
		label = "Good"
	}
	return model.Rating{Overall: overall, Label: label, Dimensions: dimensions}
}

func NormalizeFindings(findings []model.Finding) []model.Finding {
	for index := range findings {
		findings[index].ID = fmt.Sprintf("DV-%03d", index+1)
		if findings[index].Severity == model.Critical || findings[index].Severity == model.High {
			findings[index].Blocking = true
		}
		if findings[index].Severity == model.Low {
			findings[index].Blocking = false
		}
	}
	return findings
}

func Gate(rating model.Rating, findings []model.Finding, failBelow *float64, failSeverity string) model.Gate {
	reasons := []string{}
	if failBelow != nil && rating.Overall < *failBelow {
		reasons = append(reasons, fmt.Sprintf("rating %.1f is below %.1f", rating.Overall, *failBelow))
	}
	if failSeverity != "" {
		ranks := map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}
		threshold := ranks[failSeverity]
		for _, finding := range findings {
			if ranks[string(finding.Severity)] >= threshold {
				reasons = append(reasons, fmt.Sprintf("%s finding %s met the failure threshold", finding.Severity, finding.ID))
			}
		}
	}
	sort.Strings(reasons)
	return model.Gate{Passed: len(reasons) == 0, Reasons: reasons}
}

func ValidSeverity(value string) bool {
	return strings.Contains(" critical high medium low ", " "+value+" ")
}
