package memory

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type marqo struct{ *remote }

func (m marqo) put(ctx context.Context, e entry) error {
	d, err := document(e, true)
	if err != nil {
		return err
	}
	d["_id"] = e.ID
	key := "content"
	if e.Kind == "tool" {
		key = "question"
		d["args"] = d["args_json"]
		delete(d, "args_json")
		d["metadata"] = d["metadata_json"]
		delete(d, "metadata_json")
	}
	var res struct {
		Errors bool
		Items  []fields
	}
	if err := m.call(ctx, "POST", m.path+"/documents", fields{"documents": []fields{d}, "tensorFields": []string{key}}, &res); err != nil {
		return err
	}
	if res.Errors {
		return fmt.Errorf("Marqo indexing failed: %v", res.Items)
	}
	return nil
}
func (m marqo) hits(ctx context.Context, body fields) ([]fields, error) {
	var res struct{ Hits []fields }
	err := m.call(ctx, "POST", m.path+"/search", body, &res)
	return res.Hits, err
}
func (m marqo) search(ctx context.Context, q query) ([]match, error) {
	f := "success:true"
	if q.Kind == "text" {
		f = "is_text_memory:true"
	}
	if q.ToolName != "" {
		value := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(q.ToolName)
		f += " AND tool_name:(" + value + ")"
	}
	docs, err := m.hits(ctx, fields{"q": q.Text, "filter": f, "limit": q.Limit, "searchMethod": "TENSOR", "showHighlights": false})
	if err != nil {
		return nil, err
	}
	var out []match
	for _, d := range docs {
		score, _ := numeric(d["_score"])
		if err := collect(&out, "", d, score); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (m marqo) scan(ctx context.Context, fn func(entry) error) error {
	// Marqo has ranked pagination but no list-all endpoint. Exact retrieval and a
	// count check prevent filtered clears from silently operating on a partial list.
	var stats struct {
		Count int `json:"numberOfDocuments"`
	}
	if err := m.call(ctx, "GET", m.path+"/stats", nil, &stats); err != nil {
		return err
	}
	seen := map[string]bool{}
	for offset := 0; offset < stats.Count; offset += 100 {
		hits, err := m.hits(ctx, fields{"q": "*", "searchMethod": "TENSOR", "approximate": false, "limit": min(100, stats.Count-offset), "offset": offset, "showHighlights": false})
		if err != nil {
			return err
		}
		for _, d := range hits {
			id := stringField(d, "_id")
			if seen[id] {
				continue
			}
			seen[id] = true
			if err := visit(id, d, fn); err != nil {
				return err
			}
		}
	}
	if len(seen) != stats.Count {
		return fmt.Errorf("Marqo returned %d of %d documents", len(seen), stats.Count)
	}
	return nil
}
func (m marqo) get(ctx context.Context, id string) (*entry, error) {
	var d fields
	if err := m.call(ctx, "GET", m.path+"/documents/"+url.PathEscape(id), nil, &d); err != nil {
		return nil, err
	}
	return first(d, id)
}
func (m marqo) remove(ctx context.Context, id string) error {
	return m.call(ctx, "POST", m.path+"/documents/delete-batch", []string{id}, nil)
}
