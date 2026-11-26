package evaluation

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"time"
)

func (r Report) AverageTime() time.Duration {
	if len(r.Results) == 0 {
		return 0
	}
	var total time.Duration
	for _, c := range r.Results {
		total += c.Run.Duration
	}
	return total / time.Duration(len(r.Results))
}
func (r Report) TotalTokens() int {
	total := 0
	for _, c := range r.Results {
		total += c.Run.Tokens
	}
	return total
}
func (r Report) Failures() []Result {
	var failures []Result
	for _, c := range r.Results {
		if !c.Passed() {
			failures = append(failures, c)
		}
	}
	return failures
}

// BestVariant chooses the first variant on ties. An empty metric selects score.
func BestVariant(reports []Report, metric string) (string, error) {
	if len(reports) == 0 {
		return "", errors.New("no variants to compare")
	}
	var rank func(Report) float64
	switch metric {
	case "", "score":
		rank = Report.AverageScore
	case "pass_rate":
		rank = Report.PassRate
	case "speed":
		rank = func(r Report) float64 { return -float64(r.AverageTime()) }
	default:
		return "", fmt.Errorf("unknown comparison metric %q", metric)
	}
	best := reports[0]
	for _, r := range reports[1:] {
		if rank(r) > rank(best) {
			best = r
		}
	}
	return best.Variant, nil
}

func WriteSummary(w io.Writer, reports []Report) error {
	for _, r := range reports {
		if !r.Timestamp.IsZero() {
			if _, err := fmt.Fprintf(w, "Evaluation time %s\n", r.Timestamp.Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s (%d cases)  pass %.1f%%  score %.2f  average %v  tokens %d\n", r.Variant, len(r.Results), 100*r.PassRate(), r.AverageScore(), r.AverageTime(), r.TotalTokens()); err != nil {
			return err
		}
		for _, c := range r.Failures() {
			if _, err := fmt.Fprintf(w, "  %s  %s\n", c.Case.ID, c.Case.Message); err != nil {
				return err
			}
			if c.Run.Error != "" {
				if _, err := fmt.Fprintf(w, "    %s\n", c.Run.Error); err != nil {
					return err
				}
			}
			for _, e := range c.Checks {
				if !e.Passed {
					if _, err := fmt.Fprintf(w, "    [%s] %s\n", e.Name, e.Reason); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func WriteCSV(w io.Writer, reports []Report) error {
	out := csv.NewWriter(w)
	if err := out.Write([]string{"variant", "test_case_id", "test_message", "passed", "score", "execution_time_ms", "tokens", "error", "evaluator_scores"}); err != nil {
		return err
	}
	for _, r := range reports {
		for _, c := range r.Results {
			scores := map[string]float64{}
			for _, check := range c.Checks {
				scores[check.Name] = check.Score
			}
			data, err := json.Marshal(scores)
			if err != nil {
				return err
			}
			msg := []rune(c.Case.Message)
			ms := float64(c.Run.Duration) / float64(time.Millisecond)
			if err := out.Write([]string{r.Variant, c.Case.ID, string(msg[:min(50, len(msg))]), strconv.FormatBool(c.Passed()), fmt.Sprint(c.Score()), fmt.Sprint(ms), strconv.Itoa(c.Run.Tokens), c.Run.Error, string(data)}); err != nil {
				return err
			}
		}
	}
	out.Flush()
	return out.Error()
}

var htmlReport = template.Must(template.New("report").Funcs(template.FuncMap{
	"percent": func(r float64) string { return fmt.Sprintf("%.1f%%", r*100) },
	"score":   func(r float64) string { return fmt.Sprintf("%.2f", r) },
}).Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Agent comparison</title>
<style>body{font:1rem system-ui;margin:2rem auto;max-width:76rem;padding:0 1rem;color:#17232b}table{border-collapse:collapse;width:100%;margin-bottom:2rem}th,td{padding:.5rem;text-align:left;border-bottom:1px solid #cdd5da;vertical-align:top}tr.best{background:#e6f3eb}details p{white-space:pre-wrap}</style>
<h1>Agent comparison</h1>{{if .Timestamp}}<p>Generated <time>{{.Timestamp}}</time></p>{{end}}<p>Variants: {{len .Reports}} · Test cases: {{len .Cases}}</p><table><thead><tr><th>Variant<th>Pass rate<th>Average score<th>Average time<th>Tokens</thead><tbody>
{{range .Reports}}<tr{{if eq .Variant $.Best}} class="best"{{end}}><td>{{.Variant}}<td>{{percent .PassRate}}<td>{{score .AverageScore}}<td>{{.AverageTime}}<td>{{.TotalTokens}}</tr>{{end}}</tbody></table>
{{range .Cases}}<h2>{{.ID}}</h2><p>{{.Message}}</p><table><thead><tr><th>Variant<th>Result<th>Score<th>Time<th>Answer and checks</thead><tbody>
{{range .Rows}}<tr><td>{{.Variant}}<td>{{if .Result.Passed}}PASS{{else}}FAIL{{end}}<td>{{score .Result.Score}}<td>{{.Result.Run.Duration}}<td><details><summary>Details</summary><p>{{.Result.Run.Answer}}</p>{{if .Result.Run.Error}}<p>{{.Result.Run.Error}}</p>{{end}}<ul>{{range .Result.Checks}}<li>{{.Name}} · {{score .Score}} {{.Reason}}</li>{{end}}</ul></details></tr>{{end}}
</tbody></table>{{end}}</html>`))

func WriteHTML(w io.Writer, reports []Report) error {
	type row struct {
		Variant string
		Result  Result
	}
	type caseRows struct {
		ID, Message string
		Rows        []row
	}
	view := struct {
		Best, Timestamp string
		Reports         []Report
		Cases           []caseRows
	}{Reports: reports}
	if len(reports) > 0 && !reports[0].Timestamp.IsZero() {
		view.Timestamp = reports[0].Timestamp.Format(time.RFC3339Nano)
	}
	view.Best, _ = BestVariant(reports, "score")
	positions := map[string]int{}
	for _, r := range reports {
		for _, c := range r.Results {
			i, found := positions[c.Case.ID]
			if !found {
				i = len(view.Cases)
				positions[c.Case.ID] = i
				view.Cases = append(view.Cases, caseRows{ID: c.Case.ID, Message: c.Case.Message})
			}
			view.Cases[i].Rows = append(view.Cases[i].Rows, row{r.Variant, c})
		}
	}
	return htmlReport.Execute(w, view)
}
