package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
)

type Dimensions struct {
	Correctness     float64 `json:"correctness"`
	Security        float64 `json:"security"`
	Maintainability float64 `json:"maintainability"`
	Testing         float64 `json:"testing"`
	Scope           float64 `json:"scope"`
}

type Finding struct {
	ID             string   `json:"id,omitempty"`
	Severity       Severity `json:"severity"`
	Category       string   `json:"category"`
	Blocking       bool     `json:"blocking"`
	Title          string   `json:"title"`
	Explanation    string   `json:"explanation"`
	Recommendation string   `json:"recommendation"`
	Path           *string  `json:"path"`
	Line           *int     `json:"line"`
	Side           *string  `json:"side"`
	Confidence     string   `json:"confidence"`
	Evidence       string   `json:"evidence"`
}

type ProviderReview struct {
	Summary              string     `json:"summary"`
	Dimensions           Dimensions `json:"dimensions"`
	Findings             []Finding  `json:"findings"`
	PositiveObservations []string   `json:"positiveObservations"`
	NeedsVerification    []string   `json:"needsVerification"`
}

type Scope struct {
	Mode      string  `json:"mode"`
	BaseRef   string  `json:"baseRef"`
	BaseSHA   string  `json:"baseSha"`
	MergeBase *string `json:"mergeBase"`
	HeadSHA   string  `json:"headSha"`
}

type ProviderInfo struct {
	Name      string  `json:"name"`
	Transport string  `json:"transport"`
	Model     string  `json:"model"`
	Effort    *string `json:"effort"`
}

type Rating struct {
	Overall    float64    `json:"overall"`
	Label      string     `json:"label"`
	Dimensions Dimensions `json:"dimensions"`
}

type FilesSummary struct {
	Reviewed   []string `json:"reviewed"`
	Excluded   []string `json:"excluded"`
	Binary     []string `json:"binary"`
	Omitted    []string `json:"omitted"`
	Redactions int      `json:"redactions"`
}

type Gate struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
}

type Publication struct {
	Requested bool    `json:"requested"`
	Published bool    `json:"published"`
	URL       *string `json:"url"`
	Bot       *string `json:"bot"`
}

type ReviewResult struct {
	SchemaVersion        int          `json:"schemaVersion"`
	ReviewID             string       `json:"reviewId"`
	Status               string       `json:"status"`
	Partial              bool         `json:"partial"`
	Scope                Scope        `json:"scope"`
	Provider             ProviderInfo `json:"provider"`
	Rating               Rating       `json:"rating"`
	Summary              string       `json:"summary"`
	Findings             []Finding    `json:"findings"`
	PositiveObservations []string     `json:"positiveObservations"`
	NeedsVerification    []string     `json:"needsVerification"`
	Files                FilesSummary `json:"files"`
	Gate                 Gate         `json:"gate"`
	Publication          Publication  `json:"publication"`
}

func NewReviewID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("review-%d", time.Now().UnixNano())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", value[:8], value[8:12], value[12:16], value[16:20], value[20:])
}
