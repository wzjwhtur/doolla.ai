package memory

import (
	"context"
	"net/url"
)

type qdrant struct{ *remote }
type point struct {
	ID      string
	Payload fields
	Score   float64
}

func (q qdrant) put(ctx context.Context, e entry) error {
	v, err := q.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	p, err := document(e, false)
	if err != nil {
		return err
	}
	return q.call(ctx, "PUT", q.path+"?wait=true", fields{"points": []fields{{"id": e.ID, "vector": v, "payload": p}}}, nil)
}
func (q qdrant) search(ctx context.Context, x query) ([]match, error) {
	v, err := q.vector(ctx, x.Text)
	if err != nil {
		return nil, err
	}
	key := "success"
	if x.Kind == "text" {
		key = "is_text_memory"
	}
	conditions := []fields{{"key": key, "match": fields{"value": true}}}
	if x.ToolName != "" {
		conditions = append(conditions, fields{"key": "tool_name", "match": fields{"value": x.ToolName}})
	}
	var res struct{ Result struct{ Points []point } }
	err = q.call(ctx, "POST", q.path+"/query", fields{"query": v, "limit": x.Limit, "with_payload": true, "score_threshold": x.Threshold, "filter": fields{"must": conditions}}, &res)
	if err != nil {
		return nil, err
	}
	var out []match
	for _, p := range res.Result.Points {
		if err := collect(&out, p.ID, p.Payload, p.Score); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (q qdrant) scan(ctx context.Context, fn func(entry) error) error {
	var offset any
	for {
		body := fields{"limit": 100, "with_payload": true, "with_vector": false}
		if offset != nil {
			body["offset"] = offset
		}
		var res struct {
			Result struct {
				Points []point
				Next   any `json:"next_page_offset"`
			}
		}
		if err := q.call(ctx, "POST", q.path+"/scroll", body, &res); err != nil {
			return err
		}
		for _, p := range res.Result.Points {
			if err := visit(p.ID, p.Payload, fn); err != nil {
				return err
			}
		}
		offset = res.Result.Next
		if offset == nil {
			return nil
		}
	}
}
func (q qdrant) get(ctx context.Context, id string) (*entry, error) {
	var res struct{ Result point }
	if err := q.call(ctx, "GET", q.path+"/"+url.PathEscape(id), nil, &res); err != nil {
		return nil, err
	}
	return first(res.Result.Payload, id)
}
func (q qdrant) remove(ctx context.Context, id string) error {
	return q.call(ctx, "POST", q.path+"/delete?wait=true", fields{"points": []string{id}}, nil)
}
