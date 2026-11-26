package memory

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const azureVersion = "?api-version=2024-07-01"

type azure struct{ *remote }

func (a azure) index(ctx context.Context, d fields) error {
	var res struct {
		Value []struct {
			Status bool
			Error  string `json:"errorMessage"`
		}
	}
	if err := a.call(ctx, "POST", a.path+"/index"+azureVersion, fields{"value": []fields{d}}, &res); err != nil {
		return err
	}
	if len(res.Value) != 1 {
		return fmt.Errorf("Azure Search did not acknowledge document")
	}
	if !res.Value[0].Status {
		return fmt.Errorf("Azure Search document failed: %s", res.Value[0].Error)
	}
	return nil
}
func (a azure) put(ctx context.Context, e entry) error {
	v, err := a.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	d, err := document(e, true)
	if err != nil {
		return err
	}
	delete(d, "is_text_memory")
	d["memory_id"] = e.ID
	d["embedding"] = v
	d["@search.action"] = "upload"
	err = a.index(ctx, d)
	// Older Python-created indexes have no content column. Preserve them by
	// encoding a text memory in their existing question/metadata_json columns.
	if e.Kind == "text" && err != nil && strings.Contains(err.Error(), "'content'") && strings.Contains(strings.ToLower(err.Error()), "does not exist") {
		delete(d, "content")
		d["question"] = e.Content
		d["metadata_json"] = `{"is_text_memory":true}`
		return a.index(ctx, d)
	}
	return err
}
func (a azure) pages(ctx context.Context, body fields, fn func(fields) error) (int, error) {
	n := 0
	for {
		var res struct {
			Value []fields
			Next  fields `json:"@search.nextPageParameters"`
		}
		if err := a.call(ctx, "POST", a.path+"/search"+azureVersion, body, &res); err != nil {
			return n, err
		}
		for _, d := range res.Value {
			if err := fn(d); err != nil {
				return n, err
			}
			n++
		}
		if res.Next == nil {
			return n, nil
		}
		body = res.Next
	}
}
func (a azure) search(ctx context.Context, q query) ([]match, error) {
	v, err := a.vector(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	filter := "success eq true"
	if q.Kind == "text" {
		filter = "success eq null"
	}
	if q.ToolName != "" {
		filter += " and tool_name eq '" + strings.ReplaceAll(q.ToolName, "'", "''") + "'"
	}
	var out []match
	_, err = a.pages(ctx, fields{"top": q.Limit, "filter": filter, "vectorQueries": []fields{{"kind": "vector", "fields": "embedding", "vector": v, "k": q.Limit}}}, func(d fields) error { score, _ := numeric(d["@search.score"]); return collect(&out, "", d, score) })
	return out, err
}
func (a azure) scan(ctx context.Context, fn func(entry) error) error {
	for offset := 0; ; {
		n, err := a.pages(ctx, fields{"search": "*", "top": 500, "skip": offset, "orderby": "timestamp desc"}, func(d fields) error { return visit("", d, fn) })
		if err != nil {
			return err
		}
		if n < 500 {
			return nil
		}
		offset += n
	}
}
func (a azure) get(ctx context.Context, id string) (*entry, error) {
	var d fields
	if err := a.call(ctx, "GET", a.path+"/"+url.PathEscape(id)+azureVersion, nil, &d); err != nil {
		return nil, err
	}
	return first(d, id)
}
func (a azure) remove(ctx context.Context, id string) error {
	return a.index(ctx, fields{"@search.action": "delete", "memory_id": id})
}
