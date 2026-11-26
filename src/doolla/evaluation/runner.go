package evaluation

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	doolla "doolla.ai/sdk"
)

func runCase(ctx context.Context, a *doolla.Agent, c Case, evals []Evaluator) Result {
	r := Run{}

	id := c.ConversationID
	if id == "" {
		id = rand.Text()
	}
	u := c.user()
	turn := doolla.WithObserver(ctx, func(ctx context.Context, e doolla.Event) {
		r.Events = append(r.Events, e)
		if e.Operation == "tool" {
			r.Tools = append(r.Tools, e.Name)
		}
		if e.Operation == "model" && e.Usage != nil {
			r.InputTokens += e.Usage.Input
			r.OutputTokens += e.Usage.Output
			r.Tokens += e.Usage.Input + e.Usage.Output
		}
	})
	start := time.Now()
	err := a.Chat(turn, u, id, c.Message, func(x doolla.Component) error {
		r.Components = append(r.Components, x)
		if x.Type == "text" {
			if txt, ok := x.Data["content"].(string); ok {
				if r.Answer != "" {
					r.Answer += "\n"
				}
				r.Answer += txt
			}
		}
		return nil
	})
	r.Duration = time.Since(start)
	if err != nil {
		r.Error = err.Error()
	}
	result := Result{Case: c, Run: r}
	for _, eval := range evals {
		check := eval(ctx, c, r)
		check.Timestamp = time.Now()
		if r.Error != "" {
			check.Passed = false
			check.Score = 0
			check.Reason = r.Error
		}
		result.Checks = append(result.Checks, check)
	}
	return result
}

// Variant keeps the agent and its identifying metadata together in reports.
type Variant struct {
	Name     string
	Agent    *doolla.Agent
	Metadata map[string]any
}

type Progress struct {
	Variant          string
	Result           Result
	Completed, Total int
}

// Runner shares a worker limit across all variants and cases in a comparison.
// Cases within the same variant, user and explicit conversation run in order.
type Runner struct {
	MaxConcurrency int
	Evaluators     []Evaluator
}

type observabilityContextKey struct{}

// WithObservability traces the comparison and each agent variant using the
// supplied provider. Cases inherit their variant span's tracing context.
func WithObservability(ctx context.Context, provider doolla.ObservabilityProvider) context.Context {
	if provider == nil {
		return ctx
	}
	return context.WithValue(ctx, observabilityContextKey{}, provider)
}

func startSpan(ctx context.Context, provider doolla.ObservabilityProvider, name string, attrs map[string]any) (context.Context, func()) {
	if provider == nil {
		return ctx, func() {}
	}
	child, span := provider.CreateSpan(ctx, name, attrs)
	if child == nil {
		child = ctx
	}
	return child, func() {
		if span != nil {
			provider.EndSpan(child, span)
		}
	}
}

func (r Runner) Evaluate(ctx context.Context, v Variant, d Dataset) (Report, error) {
	if v.Name == "" {
		v.Name = "agent"
	}
	reports, err := r.Compare(ctx, []Variant{v}, d)
	if len(reports) == 0 {
		return Report{}, err
	}
	return reports[0], err
}

func (r Runner) Compare(ctx context.Context, variants []Variant, d Dataset) ([]Report, error) {
	return r.CompareStreaming(ctx, variants, d, nil)
}

// CompareStreaming invokes progress serially as individual cases finish.
// Returned reports always follow input order, including partial reports after cancellation.
func (r Runner) CompareStreaming(ctx context.Context, variants []Variant, d Dataset, progress func(Progress) error) ([]Report, error) {
	if r.MaxConcurrency < 0 {
		return nil, errors.New("negative evaluation concurrency")
	}
	limit := r.MaxConcurrency
	if limit == 0 {
		limit = 10
	}
	evals := r.Evaluators
	if evals == nil {
		evals = []Evaluator{Trajectory, Output, Efficiency(0, 0)}
	}
	for _, e := range evals {
		if e == nil {
			return nil, errors.New("nil evaluator")
		}
	}
	timestamp := time.Now()
	reports := make([]Report, len(variants))
	seen := map[string]bool{}
	for i, v := range variants {
		if v.Agent == nil || v.Name == "" || seen[v.Name] {
			return nil, fmt.Errorf("invalid or duplicate variant %q", v.Name)
		}
		seen[v.Name] = true
		reports[i] = Report{Variant: v.Name, Metadata: maps.Clone(v.Metadata), Timestamp: timestamp}
	}
	type job struct {
		variant int
		cases   []int
	}
	type group struct {
		variant  int
		user, id string
	}
	var jobs []job
	groups := map[group]int{}
	for index, c := range d.Cases {
		for v := range variants {
			if c.ConversationID != "" {
				key := group{v, c.user().ID, c.ConversationID}
				if j, ok := groups[key]; ok {
					jobs[j].cases = append(jobs[j].cases, index)
					continue
				}
				groups[key] = len(jobs)
			}
			jobs = append(jobs, job{v, []int{index}})
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	provider, _ := ctx.Value(observabilityContextKey{}).(doolla.ObservabilityProvider)
	ctx, end := startSpan(ctx, provider, "agent_comparison", map[string]any{"num_variants": len(variants), "num_test_cases": len(d.Cases)})
	defer end()
	variantContexts := make([]context.Context, len(variants))
	endVariant := make([]func(), len(variants))
	for i, variant := range variants {
		attrs := map[string]any{"variant": variant.Name, "num_test_cases": len(d.Cases)}
		maps.Copy(attrs, variant.Metadata)
		variantContexts[i], endVariant[i] = startSpan(ctx, provider, "variant_"+variant.Name, attrs)
	}
	defer func() {
		for _, finish := range endVariant {
			if finish != nil {
				finish()
			}
		}
	}()
	queue := make(chan job, len(jobs))
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	type completion struct {
		variant, index int
		result         Result
	}
	done := make(chan completion)
	var wg sync.WaitGroup
	for range min(limit, len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				for _, i := range j.cases {
					if ctx.Err() != nil {
						return
					}
					result := runCase(variantContexts[j.variant], variants[j.variant].Agent, d.Cases[i], evals)
					select {
					case done <- completion{j.variant, i, result}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	ordered := make([][]*Result, len(variants))
	for i := range ordered {
		ordered[i] = make([]*Result, len(d.Cases))
	}
	var callbackErr error
	finishedByVariant := make([]int, len(variants))
	completed := 0
	for x := range done {
		if ctx.Err() != nil {
			continue
		}
		ordered[x.variant][x.index] = &x.result
		finishedByVariant[x.variant]++
		if finishedByVariant[x.variant] == len(d.Cases) {
			endVariant[x.variant]()
			endVariant[x.variant] = nil
		}
		completed++
		if progress != nil {
			callbackErr = progress(Progress{variants[x.variant].Name, x.result, completed, len(variants) * len(d.Cases)})
			if callbackErr != nil {
				cancel()
			}
		}
	}
	for i, row := range ordered {
		for _, result := range row {
			if result != nil {
				reports[i].Results = append(reports[i].Results, *result)
			}
		}
	}
	if callbackErr != nil {
		return reports, callbackErr
	}
	return reports, ctx.Err()
}

// Evaluate keeps the simple sequential API for callers that provide a shared evaluator.
func Evaluate(ctx context.Context, name string, a *doolla.Agent, d Dataset, evals ...Evaluator) Report {
	report, _ := (Runner{1, evals}).Evaluate(ctx, Variant{Name: name, Agent: a}, d)
	return report
}

func Compare(ctx context.Context, variants map[string]*doolla.Agent, d Dataset, evals ...Evaluator) []Report {
	var ordered []Variant
	for _, name := range slices.Sorted(maps.Keys(variants)) {
		ordered = append(ordered, Variant{Name: name, Agent: variants[name]})
	}
	reports, _ := (Runner{1, evals}).Compare(ctx, ordered, d)
	return reports
}
