package memory

import (
	"context"
	"fmt"
	"net/url"
)

type pinecone struct{ *remote }
type pineVector struct {
	ID       string
	Metadata fields
	Score    float64
}

func (p pinecone) put(ctx context.Context, e entry) error {
	v, err := p.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	m, err := document(e, true)
	if err != nil {
		return err
	}
	var response struct {
		Count int `json:"upsertedCount"`
	}
	if err := p.call(ctx, "POST", "/vectors/upsert", fields{"namespace": p.Namespace, "vectors": []fields{{"id": e.ID, "values": v, "metadata": m}}}, &response); err != nil {
		return err
	}
	if response.Count != 1 {
		return fmt.Errorf("Pinecone acknowledged %d vectors, expected one", response.Count)
	}
	return nil
}
func (p pinecone) search(ctx context.Context, q query) ([]match, error) {
	v, err := p.vector(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	f := fields{"success": true}
	if q.Kind == "text" {
		f = fields{"is_text_memory": true}
	}
	if q.ToolName != "" {
		f["tool_name"] = q.ToolName
	}
	var res struct{ Matches []pineVector }
	if err := p.call(ctx, "POST", "/query", fields{"vector": v, "namespace": p.Namespace, "topK": q.Limit, "includeMetadata": true, "filter": f}, &res); err != nil {
		return nil, err
	}
	var out []match
	for _, m := range res.Matches {
		if err := collect(&out, m.ID, m.Metadata, m.Score); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (p pinecone) fetch(ctx context.Context, ids []string) (map[string]pineVector, error) {
	params := url.Values{"namespace": {p.Namespace}, "ids": ids}
	var res struct{ Vectors map[string]pineVector }
	err := p.call(ctx, "GET", "/vectors/fetch?"+params.Encode(), nil, &res)
	return res.Vectors, err
}
func (p pinecone) scan(ctx context.Context, fn func(entry) error) error {
	params := url.Values{"namespace": {p.Namespace}, "limit": {"100"}}
	for {
		var res struct {
			Vectors    []pineVector
			Pagination struct{ Next string }
		}
		if err := p.call(ctx, "GET", "/vectors/list?"+params.Encode(), nil, &res); err != nil {
			return err
		}
		var ids []string
		for _, v := range res.Vectors {
			ids = append(ids, v.ID)
		}
		if len(ids) > 0 {
			vecs, err := p.fetch(ctx, ids)
			if err != nil {
				return err
			}
			for id, v := range vecs {
				if err := visit(id, v.Metadata, fn); err != nil {
					return err
				}
			}
		}
		if res.Pagination.Next == "" {
			return nil
		}
		params.Set("paginationToken", res.Pagination.Next)
	}
}
func (p pinecone) get(ctx context.Context, id string) (*entry, error) {
	v, err := p.fetch(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	return first(v[id].Metadata, id)
}
func (p pinecone) remove(ctx context.Context, id string) error {
	return p.call(ctx, "POST", "/vectors/delete", fields{"ids": []string{id}, "namespace": p.Namespace}, nil)
}
