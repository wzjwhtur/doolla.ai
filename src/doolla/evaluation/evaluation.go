// Package evaluation runs existing Doolla JSON/YAML datasets against Go agents.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	doolla "doolla.ai/sdk"
	"gopkg.in/yaml.v3"
)

type Expected struct {
	ToolsCalled    []string       `json:"tools_called,omitempty" yaml:"tools_called,omitempty"`
	ToolsNotCalled []string       `json:"tools_not_called,omitempty" yaml:"tools_not_called,omitempty"`
	Contains       []string       `json:"final_answer_contains,omitempty" yaml:"final_answer_contains,omitempty"`
	Excludes       []string       `json:"final_answer_not_contains,omitempty" yaml:"final_answer_not_contains,omitempty"`
	MinComponents  *int           `json:"min_components,omitempty" yaml:"min_components,omitempty"`
	MaxComponents  *int           `json:"max_components,omitempty" yaml:"max_components,omitempty"`
	MaxTime        float64        `json:"max_execution_time_ms,omitempty" yaml:"max_execution_time_ms,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}
type Case struct {
	ID             string         `json:"id" yaml:"id"`
	Message        string         `json:"message" yaml:"message"`
	UserID         string         `json:"user_id,omitempty" yaml:"user_id,omitempty"`
	Username       string         `json:"username,omitempty" yaml:"username,omitempty"`
	Email          string         `json:"email,omitempty" yaml:"email,omitempty"`
	Groups         []string       `json:"user_groups,omitempty" yaml:"user_groups,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty" yaml:"conversation_id,omitempty"`
	Expected       Expected       `json:"expected_outcome,omitempty" yaml:"expected_outcome,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}
type Dataset struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Cases       []Case `json:"test_cases" yaml:"test_cases"`
}

func Load(path string) (Dataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Dataset{}, err
	}
	// YAML accepts JSON as a subset, but JSON datasets must follow JSON syntax.
	if strings.EqualFold(filepath.Ext(path), ".json") && !json.Valid(data) {
		return Dataset{}, errors.New("invalid JSON evaluation dataset")
	}
	var wrapped struct {
		Dataset *Dataset `yaml:"dataset"`
	}
	if err = yaml.Unmarshal(data, &wrapped); err != nil {
		return Dataset{}, err
	}
	var d Dataset
	if wrapped.Dataset != nil {
		d = *wrapped.Dataset
	} else if err := yaml.Unmarshal(data, &d); err != nil {
		return d, err
	}
	if d.Name == "" {
		d.Name = "Unnamed Dataset"
	}
	for i, c := range d.Cases {
		u := c.user()
		d.Cases[i].UserID = u.ID
		d.Cases[i].Username = u.Username
		d.Cases[i].Email = u.Email
		if c.ID == "" || c.Message == "" {
			return d, fmt.Errorf("missing id or message for case %d", i)
		}
	}
	return d, nil
}
func (c Case) user() doolla.User {
	u := doolla.User{ID: c.UserID, Username: c.Username, Email: c.Email, Groups: c.Groups}
	if u.ID == "" {
		u.ID = "test_user"
	}
	if u.Username == "" {
		u.Username = u.ID
	}
	if u.Email == "" {
		u.Email = u.ID + "@example.com"
	}
	return u
}

// FilterByMetadata selects cases matching all supplied fields, in dataset order.
func (d Dataset) FilterByMetadata(fields map[string]any) Dataset {
	filtered := Dataset{Name: d.Name + " (filtered)", Description: "Filtered from: " + d.Description}
	for _, c := range d.Cases {
		match := true
		for k, v := range fields {
			if !reflect.DeepEqual(c.Metadata[k], v) {
				match = false
				break
			}
		}
		if match {
			filtered.Cases = append(filtered.Cases, c)
		}
	}
	return filtered
}
func (d Dataset) Save(path string) error {
	if d.Cases == nil {
		d.Cases = []Case{}
	}
	wrapped := map[string]Dataset{"dataset": d}
	var data []byte
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		data, err = json.MarshalIndent(wrapped, "", "  ")
	case ".yaml", ".yml":
		data, err = yaml.Marshal(wrapped)
	default:
		return errors.New("expected .json, .yaml or .yml")
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

type Run struct {
	Events       []doolla.Event
	InputTokens  int
	OutputTokens int
	Answer       string
	Tools        []string
	Components   []doolla.Component
	Duration     time.Duration
	Tokens       int
	Error        string
}
type Check struct {
	Metrics   map[string]any
	Timestamp time.Time
	Name      string
	Passed    bool
	Score     float64
	Reason    string
}
type Evaluator func(context.Context, Case, Run) Check
type Result struct {
	Case   Case
	Run    Run
	Checks []Check
}

func (r Result) Passed() bool {
	if r.Run.Error != "" {
		return false
	}
	for _, e := range r.Checks {
		if !e.Passed {
			return false
		}
	}
	return true
}
func (r Result) Score() float64 {
	if len(r.Checks) == 0 {
		return 0
	}
	var sum float64
	for _, c := range r.Checks {
		sum += c.Score
	}
	return sum / float64(len(r.Checks))
}

type Report struct {
	Metadata  map[string]any
	Timestamp time.Time
	Variant   string
	Results   []Result
}

func (r Report) PassRate() float64 {
	if len(r.Results) == 0 {
		return 0
	}
	n := 0
	for _, c := range r.Results {
		if c.Passed() {
			n++
		}
	}
	return float64(n) / float64(len(r.Results))
}
func (r Report) AverageScore() float64 {
	if len(r.Results) == 0 {
		return 0
	}
	sum := 0.
	for _, c := range r.Results {
		sum += c.Score()
	}
	return sum / float64(len(r.Results))
}
func evaluateSet(name string, yes, no []string, found func(string) bool) Check {
	score := 1.
	var issues []string
	for _, want := range yes {
		if !found(want) {
			score -= .5 / float64(len(yes))
			issues = append(issues, "missing "+want)
		}
	}
	for _, bad := range no {
		if found(bad) {
			score -= .5 / float64(len(no))
			issues = append(issues, "unexpected "+bad)
		}
	}
	return Check{Name: name, Passed: score >= .7, Score: math.Max(0, score), Reason: strings.Join(issues, ", "), Metrics: map[string]any{"issues": issues}}
}
func Trajectory(ctx context.Context, c Case, r Run) Check {
	if r.Error != "" {
		return Check{Name: "trajectory", Reason: r.Error}
	}
	check := evaluateSet("trajectory", c.Expected.ToolsCalled, c.Expected.ToolsNotCalled, func(s string) bool { return slices.Contains(r.Tools, s) })
	check.Metrics["tools_called"] = slices.Clone(r.Tools)
	check.Metrics["num_tools_called"] = len(r.Tools)
	return check
}
func Output(ctx context.Context, c Case, r Run) Check {
	if r.Error != "" {
		return Check{Name: "output", Reason: r.Error}
	}
	answer := strings.ToLower(r.Answer)
	check := evaluateSet("output", c.Expected.Contains, c.Expected.Excludes, func(s string) bool { return strings.Contains(answer, strings.ToLower(s)) })
	check.Metrics["output_length"] = len([]rune(r.Answer))
	n := len(r.Components)
	if c.Expected.MinComponents != nil && n < *c.Expected.MinComponents || c.Expected.MaxComponents != nil && n > *c.Expected.MaxComponents {
		check.Score = 0
		check.Passed = false
		check.Reason += fmt.Sprintf(" component count %d outside requested range", n)
	}
	return check
}

// EfficiencyBudget optionally uses an application-provided price function.
type EfficiencyBudget struct {
	MaxTime    time.Duration
	MaxTokens  int
	MaxCostUSD float64
	CostUSD    func(Run) (float64, bool)
}

func Efficiency(maxTime time.Duration, maxTokens int) Evaluator {
	return EfficiencyBudget{MaxTime: maxTime, MaxTokens: maxTokens}.Evaluate
}

func (b EfficiencyBudget) Evaluate(ctx context.Context, c Case, r Run) Check {
	score := 1.0
	var issues []string
	metrics := map[string]any{"execution_time_ms": float64(r.Duration) / float64(time.Millisecond), "total_tokens": r.Tokens}
	if b.MaxTime > 0 && r.Duration > b.MaxTime {
		score -= .33
		issues = append(issues, "duration exceeded")
	}
	if b.MaxTokens > 0 && r.Tokens > b.MaxTokens {
		score -= .33
		issues = append(issues, "token limit exceeded")
	}
	if c.Expected.MaxTime > 0 && float64(r.Duration)/float64(time.Millisecond) > c.Expected.MaxTime {
		score -= .34
		issues = append(issues, "case duration exceeded")
	}
	if b.CostUSD != nil {
		if cost, ok := b.CostUSD(r); ok {
			metrics["cost_usd"] = cost
			if b.MaxCostUSD > 0 && cost > b.MaxCostUSD {
				score -= .33
				issues = append(issues, "cost limit exceeded")
			}
		}
	}
	metrics["issues"] = issues
	return Check{Name: "efficiency", Passed: score >= .7, Score: math.Max(0, score), Reason: strings.Join(issues, ", "), Metrics: metrics}
}
func Judge(model doolla.Model, criteria string) Evaluator {
	return func(ctx context.Context, c Case, r Run) Check {
		if r.Error != "" {
			return Check{Name: "llm_judge", Reason: r.Error}
		}
		if model == nil {
			return Check{Name: "llm_judge", Reason: "judge model required"}
		}
		data, _ := json.Marshal(map[string]any{"question": c.Message, "answer": r.Answer, "tools": r.Tools})
		reply, err := model.Reply(ctx, []doolla.Message{{Role: "system", Content: "Evaluate using these criteria: " + criteria + ". Return JSON fields score (0 to 1), passed (true when score >= 0.7), and reasoning (string)."}, {Role: "user", Content: string(data)}}, nil)
		if err != nil {
			return Check{Name: "llm_judge", Reason: err.Error()}
		}
		var result struct {
			Score     *float64
			Passed    *bool
			Reasoning string
		}
		check := Check{Name: "llm_judge", Metrics: map[string]any{"judge_response": reply.Content}}
		if err := json.Unmarshal([]byte(reply.Content), &result); err == nil && result.Score != nil && result.Passed != nil && *result.Score >= 0 && *result.Score <= 1 {
			check.Score, check.Passed, check.Reason = *result.Score, *result.Passed, result.Reasoning
			return check
		}
		// Older judges use the line-oriented response format of the Python runner.
		result.Score = nil
		result.Passed = nil
		check.Reason = reply.Content
		formatted := false
		for _, line := range strings.Split(reply.Content, "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.ToUpper(strings.TrimSpace(k)) {
			case "SCORE":
				formatted = true
				score, err := strconv.ParseFloat(v, 64)
				if err == nil && score >= 0 && score <= 1 {
					result.Score = &score
				}
			case "PASSED":
				formatted = true
				yes := slices.Contains([]string{"yes", "true", "pass"}, strings.ToLower(v))
				result.Passed = &yes
			case "REASONING":
				formatted = true
				check.Reason = v
			}
		}
		if formatted {
			check.Score = 0.5
			if result.Score != nil {
				check.Score = *result.Score
			}
			if result.Passed != nil {
				check.Passed = *result.Passed
			}
		}
		return check
	}
}
