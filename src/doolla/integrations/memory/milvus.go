package memory

import (
	"context"
	"fmt"
	"strconv"
)

type milvus struct{ *remote }

var milvusFields = []string{"id", "question", "tool_name", "args_json", "metadata_json", "timestamp", "success"}

func (m milvus) request(ctx context.Context, action string, b fields) ([]fields, error) {
	b["collectionName"] = m.Collection
	if m.Database != "" {
		b["dbName"] = m.Database
	}
	// Mutations return data objects; queries return row arrays.
	var res struct {
		Code    int
		Message string
		Data    any
	}
	if err := m.call(ctx, "POST", "/v2/vectordb/entities/"+action, b, &res); err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("Milvus %s (%d): %s", action, res.Code, res.Message)
	}
	var rows []fields
	if values, ok := res.Data.([]any); ok {
		for _, v := range values {
			d, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unexpected Milvus %s result", action)
			}
			rows = append(rows, d)
		}
	}
	return rows, nil
}
func (m milvus) put(ctx context.Context, e entry) error {
	v, err := m.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	d, err := packedToolDocument(e)
	if err != nil {
		return err
	}
	d["id"] = e.ID
	d["embedding"] = v
	_, err = m.request(ctx, "upsert", fields{"data": []fields{d}})
	return err
}
func packedToolDocument(e entry) (fields, error) {
	d, err := document(e, true)
	if err != nil {
		return nil, err
	}
	if e.Kind == "text" {
		delete(d, "content")
		delete(d, "is_text_memory")
		d["question"] = e.Content
		d["tool_name"] = ""
		d["success"] = true
		// Other clients decode this field as JSON even for text memories.
		d["args_json"] = "{}"
		d["metadata_json"] = `{"is_text_memory":true}`
	}
	return d, nil
}

// In the shared Milvus/Weaviate schema an empty tool_name is the type
// discriminator, including for historical rows without a metadata marker.
func unpackToolDocument(d fields) fields {
	if d != nil && stringField(d, "tool_name") == "" {
		d["is_text_memory"] = true
	}
	return d
}

func (m milvus) search(ctx context.Context, q query) ([]match, error) {
	v, err := m.vector(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	filter := `success == true and tool_name != ""`
	if q.Kind == "text" {
		filter = `tool_name == ""`
	}
	if q.ToolName != "" {
		filter += " and tool_name == " + strconv.Quote(q.ToolName)
	}
	rows, err := m.request(ctx, "search", fields{"annsField": "embedding", "data": [][]float32{v}, "filter": filter, "limit": q.Limit, "outputFields": milvusFields, "consistencyLevel": "Strong"})
	if err != nil {
		return nil, err
	}
	var out []match
	for _, d := range rows {
		score, _ := numeric(d["distance"])
		if err := collect(&out, "", unpackToolDocument(d), score); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (m milvus) scan(ctx context.Context, fn func(entry) error) error {
	for offset := 0; ; offset += 100 {
		rows, err := m.request(ctx, "query", fields{"filter": `id != ""`, "offset": offset, "limit": 100, "outputFields": milvusFields, "consistencyLevel": "Strong"})
		if err != nil {
			return err
		}
		for _, d := range rows {
			if err := visit("", unpackToolDocument(d), fn); err != nil {
				return err
			}
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
func (m milvus) get(ctx context.Context, id string) (*entry, error) {
	rows, err := m.request(ctx, "get", fields{"id": []string{id}, "outputFields": milvusFields, "consistencyLevel": "Strong"})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return first(unpackToolDocument(rows[0]), id)
}
func (m milvus) remove(ctx context.Context, id string) error {
	_, err := m.request(ctx, "delete", fields{"filter": "id == " + strconv.Quote(id)})
	return err
}
