package memory

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

type premium struct{ *remote }

func (p premium) Save(ctx context.Context, e entry) error {
	e, err := prepare(e)
	if err != nil {
		return err
	}
	if e.Kind == "text" {
		return errors.New("Premium does not support text memories")
	}
	d, err := document(e, false)
	if err != nil {
		return err
	}
	d["id"] = e.ID
	return p.call(ctx, "POST", "/memory/tool-usage", d, nil)
}
func (p premium) Search(ctx context.Context, q query) ([]match, error) {
	if q.Kind != "tool" || q.Limit <= 0 {
		return nil, nil
	}
	v := url.Values{"question": {q.Text}, "limit": {strconv.Itoa(q.Limit)}, "similarity_threshold": {strconv.FormatFloat(q.Threshold, 'g', -1, 64)}}
	if q.ToolName != "" {
		v.Set("tool_name_filter", q.ToolName)
	}
	var data struct {
		Results []struct {
			Memory fields
			Rank   int
			Score  float64 `json:"similarity_score"`
		}
	}
	if err := p.call(ctx, "GET", "/memory/search-similar?"+v.Encode(), nil, &data); err != nil {
		return nil, err
	}
	var out []match
	for _, m := range data.Results {
		e, err := readDocument("", m.Memory)
		if err != nil {
			return nil, err
		}
		rank := m.Rank
		if rank <= 0 {
			rank = len(out) + 1
		}
		out = append(out, match{Memory: e, Score: m.Score, Rank: rank})
	}
	return out, nil
}
func (p premium) Recent(ctx context.Context, kind string, limit int) ([]entry, error) {
	if kind != "tool" || limit <= 0 {
		return nil, nil
	}
	var res struct{ Memories []fields }
	if err := p.call(ctx, "GET", "/memory/recent?limit="+strconv.Itoa(limit), nil, &res); err != nil {
		return nil, err
	}
	var out []entry
	for _, m := range res.Memories {
		e, err := readDocument("", m)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
func (p premium) Delete(ctx context.Context, kind, id string) (bool, error) {
	if kind != "tool" {
		return false, nil
	}
	err := p.call(ctx, "DELETE", "/memory/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, errMissing) {
		return false, nil
	}
	return err == nil, err
}
func (p premium) Clear(ctx context.Context, tool string, before time.Time) (int, error) {
	d := fields{}
	if tool != "" {
		d["tool_name"] = tool
	}
	if !before.IsZero() {
		d["before_date"] = before.UTC().Format(time.RFC3339Nano)
	}
	var res struct {
		Count int `json:"deleted_count"`
	}
	err := p.call(ctx, "DELETE", "/memory/clear", d, &res)
	return res.Count, err
}
