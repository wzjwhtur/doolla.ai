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
	// Legacy text rows store the marker in metadata_json. Empty tool_name
	// comparisons have no tokens in existing word-tokenized Weaviate schemas.
	marker := strconv.Quote(`{"is_text_memory": true}`)
	compactMarker := strconv.Quote(`{"is_text_memory":true}`)
	metadataFilter := func(operator, value string) string {
		return `{path:["metadata_json"],operator:` + operator + `,valueText:` + value + `}`
	}
	where := `{operator:And,operands:[{path:["success"],operator:Equal,valueBoolean:true},` + metadataFilter("NotEqual", marker) + `,` + metadataFilter("NotEqual", compactMarker) + `]}`
	if q.Kind == "text" {
		where = `{operator:Or,operands:[` + metadataFilter("Equal", marker) + `,` + metadataFilter("Equal", compactMarker) + `]}`
	}
	if q.ToolName != "" {
		where = `{operator:And,operands:[` + where + `,{path:["tool_name"],operator:Equal,valueText:` + strconv.Quote(q.ToolName) + `}]}`
	}
	gql := fmt.Sprintf("{Get{%s(nearVector:{vector:%s},where:%s,limit:%d){question tool_name args_json metadata_json timestamp success _additional{id distance}}}}", w.Collection, encoded, where, q.Limit)
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
	var out []match
	for _, d := range res.Data.Get[w.Collection] {
		a, _ := d["_additional"].(map[string]any)
		dist, ok := numeric(a["distance"])
		if !ok {
			return nil, fmt.Errorf("Weaviate result missing distance")
		}
		if err := collect(&out, stringField(a, "id"), d, 1-dist); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (w weaviate) scan(ctx context.Context, fn func(entry) error) error {
	params := url.Values{"class": {w.Collection}, "limit": {"100"}}
	for {
		var res struct{ Objects []weavObject }
		if err := w.call(ctx, "GET", "/v1/objects?"+params.Encode(), nil, &res); err != nil {
			return err
		}
		for _, o := range res.Objects {
			if err := visit(o.ID, o.Properties, fn); err != nil {
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
	return first(o.Properties, id)
}
func (w weaviate) remove(ctx context.Context, id string) error {
	return w.call(ctx, "DELETE", "/v1/objects/"+w.Collection+"/"+url.PathEscape(id), nil, nil)
}
