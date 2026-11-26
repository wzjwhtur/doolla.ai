package memory

import (
	"context"
	"math"
	"net/url"
)

type openSearch struct{ *remote }
type openHit struct {
	ID     string  `json:"_id"`
	Score  float64 `json:"_score"`
	Source fields  `json:"_source"`
}
type openHits struct {
	Scroll string `json:"_scroll_id"`
	Hits   struct{ Hits []openHit }
}

func (o openSearch) put(ctx context.Context, e entry) error {
	v, err := o.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	d, err := document(e, false)
	if err != nil {
		return err
	}
	d["embedding"] = v
	d["memory_id"] = e.ID
	return o.call(ctx, "PUT", o.path+"/_doc/"+url.PathEscape(e.ID)+"?refresh=true", d, nil)
}
func (o openSearch) search(ctx context.Context, q query) ([]match, error) {
	v, err := o.vector(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	key := "success"
	if q.Kind == "text" {
		key = "is_text_memory"
	}
	filter := []fields{{"term": fields{key: true}}}
	if q.ToolName != "" {
		filter = append(filter, fields{"term": fields{"tool_name": q.ToolName}})
	}
	// Explicitly shift cosine similarity into OpenSearch's positive score range.
	// This keeps scores independent of engine- and version-specific scaling.
	script := fields{"source": "1.0 + cosineSimilarity(params.q, doc[params.field])", "lang": "painless", "params": fields{"field": "embedding", "q": v}}
	body := fields{"size": q.Limit, "_source": fields{"excludes": []string{"embedding"}}, "query": fields{"script_score": fields{"query": fields{"bool": fields{"filter": filter}}, "script": script}}}
	var res openHits
	if err := o.call(ctx, "POST", o.path+"/_search", body, &res); err != nil {
		return nil, err
	}
	var out []match
	for _, h := range res.Hits.Hits {
		if err := collect(&out, h.ID, h.Source, math.Max(0, math.Min(h.Score-1, 1))); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (o openSearch) scan(ctx context.Context, fn func(entry) error) error {
	var res openHits
	err := o.call(ctx, "POST", o.path+"/_search?scroll=1m", fields{"size": 100, "sort": []string{"_doc"}, "_source": fields{"excludes": []string{"embedding"}}}, &res)
	if err != nil {
		return err
	}
	cursor := res.Scroll
	defer func() {
		if cursor != "" {
			_ = o.call(ctx, "DELETE", "/_search/scroll", fields{"scroll_id": []string{cursor}}, nil)
		}
	}()
	for len(res.Hits.Hits) > 0 {
		for _, h := range res.Hits.Hits {
			if err := visit(h.ID, h.Source, fn); err != nil {
				return err
			}
		}
		if cursor == "" {
			return nil
		}
		res = openHits{}
		if err := o.call(ctx, "POST", "/_search/scroll", fields{"scroll": "1m", "scroll_id": cursor}, &res); err != nil {
			return err
		}
		if res.Scroll != "" {
			cursor = res.Scroll
		}
	}
	return nil
}
func (o openSearch) get(ctx context.Context, id string) (*entry, error) {
	var h openHit
	if err := o.call(ctx, "GET", o.path+"/_doc/"+url.PathEscape(id), nil, &h); err != nil {
		return nil, err
	}
	return first(h.Source, id)
}
func (o openSearch) remove(ctx context.Context, id string) error {
	return o.call(ctx, "DELETE", o.path+"/_doc/"+url.PathEscape(id)+"?refresh=true", nil, nil)
}
