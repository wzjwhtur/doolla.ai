package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

type weaviate struct{ *remote }
type weavObject struct {
	ID         string
	Properties fields
}

func (w weaviate) put(ctx context.Context, e entry) error {
	v, err := w.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	d, err := packedToolDocument(e)
	if err != nil {
		return err
	}
	// The batch objects endpoint supports idempotent replacement with explicit UUIDs.
	var res []struct {
		Result struct {
			Status string
			Errors any
		}
	}
	err = w.call(ctx, "POST", "/v1/batch/objects", fields{"objects": []fields{{"class": w.Collection, "id": e.ID, "properties": d, "vector": v}}}, &res)
	if err != nil {
		return err
	}
	if len(res) != 1 || res[0].Result.Status != "SUCCESS" {
		return fmt.Errorf("Weaviate batch insert failed: %v", res)
	}
	return nil
}
func (w weaviate) search(ctx context.Context, q query) ([]match, error) {
	v, err := w.vector(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// The legacy word tokenizer cannot filter reliably on the empty string.
	// Rank candidates on the server, then classify by the stored tool name.
	where := ""
	if q.Kind == "tool" {
		predicate := `{path:["success"],operator:Equal,valueBoolean:true}`
		if q.ToolName != "" {
			predicate = `{operator:And,operands:[` + predicate + `,{path:["tool_name"],operator:Equal,valueText:` + strconv.Quote(q.ToolName) + `}]}`
		}
		where = ",where:" + predicate
	}
	var out []match
	seen := make(map[string]bool)
	pageSize := max(q.Limit, 32)
	for offset := 0; ; offset += pageSize {
		gql := fmt.Sprintf("{Get{%s(nearVector:{vector:%s}%s,limit:%d,offset:%d){question tool_name args_json metadata_json timestamp success _additional{id distance}}}}", w.Collection, encoded, where, pageSize, offset)
		page, err := w.searchPage(ctx, gql)
		if err != nil {
			return nil, err
		}
		for _, d := range page {
			a, _ := d["_additional"].(map[string]any)
			dist, ok := numeric(a["distance"])
			if !ok {
				return nil, fmt.Errorf("Weaviate result missing distance")
			}
			score := 1 - dist
			if score < q.Threshold {
				return out, nil
			}
			name := stringField(d, "tool_name")
			if (name == "") != (q.Kind == "text") || (q.ToolName != "" && name != q.ToolName) {
				continue
			}
			id := stringField(a, "id")
			if seen[id] {
				continue
			}
			if err := collect(&out, id, unpackToolDocument(d), score); err != nil {
				return nil, err
			}
			seen[id] = true
			if len(out) == q.Limit {
				return out, nil
			}
		}
		if len(page) < pageSize {
			return out, nil
		}
	}
}

func (w weaviate) searchPage(ctx context.Context, gql string) ([]fields, error) {
	var res struct {
		Data   struct{ Get map[string][]fields }
		Errors []struct{ Message string }
	}
	if err := w.call(ctx, "POST", "/v1/graphql", fields{"query": gql}, &res); err != nil {
		return nil, err
	}
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("Weaviate search: %s", res.Errors[0].Message)
	}
	return res.Data.Get[w.Collection], nil
}
func (w weaviate) scan(ctx context.Context, fn func(entry) error) error {
	params := url.Values{"class": {w.Collection}, "limit": {"100"}}
	for {
		var res struct{ Objects []weavObject }
		if err := w.call(ctx, "GET", "/v1/objects?"+params.Encode(), nil, &res); err != nil {
			return err
		}
		for _, o := range res.Objects {
			if err := visit(o.ID, unpackToolDocument(o.Properties), fn); err != nil {
				return err
			}
		}
		if len(res.Objects) < 100 {
			return nil
		}
		params.Set("after", res.Objects[len(res.Objects)-1].ID)
	}
}
func (w weaviate) get(ctx context.Context, id string) (*entry, error) {
	var o weavObject
	if err := w.call(ctx, "GET", "/v1/objects/"+w.Collection+"/"+url.PathEscape(id), nil, &o); err != nil {
		return nil, err
	}
	return first(unpackToolDocument(o.Properties), id)
}
func (w weaviate) remove(ctx context.Context, id string) error {
	return w.call(ctx, "DELETE", "/v1/objects/"+w.Collection+"/"+url.PathEscape(id), nil, nil)
}
