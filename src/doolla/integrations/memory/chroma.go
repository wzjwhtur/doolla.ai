package memory

import (
	"context"
	"errors"
	"math"
)

type chroma struct{ *remote }

func (c chroma) put(ctx context.Context, e entry) error {
	v, err := c.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	m, err := document(e, true)
	if err != nil {
		return err
	}
	return c.call(ctx, "POST", c.path+"/upsert", fields{"ids": []string{e.ID}, "embeddings": [][]float32{v}, "documents": []string{textOf(e)}, "metadatas": []fields{m}}, nil)
}
func (c chroma) search(ctx context.Context, q query) ([]match, error) {
	v, err := c.vector(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	f := fields{"success": true}
	if q.Kind == "text" {
		f = fields{"is_text_memory": true}
	}
	if q.ToolName != "" {
		f = fields{"$and": []fields{f, {"tool_name": q.ToolName}}}
	}
	var res struct {
		IDs       [][]string
		Metadatas [][]fields
		Distances [][]float64
	}
	if err := c.call(ctx, "POST", c.path+"/query", fields{"query_embeddings": [][]float32{v}, "n_results": q.Limit, "where": f, "include": []string{"metadatas", "distances"}}, &res); err != nil {
		return nil, err
	}
	if len(res.IDs) == 0 || len(res.IDs[0]) == 0 {
		return nil, nil
	}
	if len(res.Metadatas) != 1 || len(res.Distances) != 1 || len(res.IDs[0]) != len(res.Metadatas[0]) || len(res.Distances[0]) != len(res.IDs[0]) {
		return nil, errors.New("inconsistent Chroma query arrays")
	}
	var out []match
	for i, id := range res.IDs[0] {
		if err := collect(&out, id, res.Metadatas[0][i], math.Max(0, 1-res.Distances[0][i])); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (c chroma) records(ctx context.Context, b fields) ([]entry, error) {
	b["include"] = []string{"metadatas"}
	var r struct {
		IDs       []string
		Metadatas []fields
	}
	if err := c.call(ctx, "POST", c.path+"/get", b, &r); err != nil {
		return nil, err
	}
	if len(r.IDs) != len(r.Metadatas) {
		return nil, errors.New("inconsistent Chroma result arrays")
	}
	var out []entry
	for i, id := range r.IDs {
		e, err := readDocument(id, r.Metadatas[i])
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
func (c chroma) scan(ctx context.Context, fn func(entry) error) error {
	for offset := 0; ; offset += 100 {
		rows, err := c.records(ctx, fields{"offset": offset, "limit": 100})
		if err != nil {
			return err
		}
		for _, e := range rows {
			if err := fn(e); err != nil {
				return err
			}
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
func (c chroma) get(ctx context.Context, id string) (*entry, error) {
	rows, err := c.records(ctx, fields{"ids": []string{id}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}
func (c chroma) remove(ctx context.Context, id string) error {
	return c.call(ctx, "POST", c.path+"/delete", fields{"ids": []string{id}}, nil)
}
