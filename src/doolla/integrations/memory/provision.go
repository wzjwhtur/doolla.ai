package memory

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (r *remote) provision(ctx context.Context) error {
	var lookup, create, method string
	var schema fields
	switch r.Provider {
	case "chroma", "chromadb", "premium":
		return nil
	case "qdrant":
		lookup = "/collections/" + r.Collection
		create = lookup
		method = "PUT"
		schema = fields{"vectors": fields{"size": r.Dimension, "distance": "Cosine"}}
	case "azuresearch":
		lookup = "/indexes/" + r.Collection + azureVersion
		create = "/indexes" + azureVersion
		method = "POST"
		attributes := []fields{{"name": "memory_id", "type": "Edm.String", "key": true, "filterable": true, "sortable": true}, {"name": "question", "type": "Edm.String", "searchable": true}, {"name": "content", "type": "Edm.String", "searchable": true}, {"name": "tool_name", "type": "Edm.String", "filterable": true}, {"name": "timestamp", "type": "Edm.String", "filterable": true, "sortable": true}, {"name": "success", "type": "Edm.Boolean", "filterable": true}, {"name": "args_json", "type": "Edm.String"}, {"name": "metadata_json", "type": "Edm.String"}, {"name": "embedding", "type": "Collection(Edm.Single)", "searchable": true, "dimensions": r.Dimension, "vectorSearchProfile": "default"}}
		schema = fields{"name": r.Collection, "fields": attributes, "vectorSearch": fields{"profiles": []fields{{"name": "default", "algorithm": "hnsw"}}, "algorithms": []fields{{"name": "hnsw", "kind": "hnsw", "hnswParameters": fields{"metric": "cosine"}}}}}
	case "opensearch":
		lookup = r.path
		create = lookup
		method = "PUT"
		props := fields{"memory_id": fields{"type": "keyword"}, "question": fields{"type": "text"}, "content": fields{"type": "text"}, "tool_name": fields{"type": "keyword"}, "args": fields{"type": "object", "enabled": false}, "metadata": fields{"type": "object", "enabled": false}, "timestamp": fields{"type": "date"}, "success": fields{"type": "boolean"}, "is_text_memory": fields{"type": "boolean"}, "embedding": fields{"type": "knn_vector", "dimension": r.Dimension, "method": fields{"name": "hnsw", "engine": "lucene", "space_type": "cosinesimil"}}}
		schema = fields{"settings": fields{"index.knn": true}, "mappings": fields{"properties": props}}
	case "weaviate":
		lookup = "/v1/schema/" + r.Collection
		create = "/v1/schema"
		method = "POST"
		var props []fields
		for _, name := range []string{"question", "tool_name", "args_json", "metadata_json", "timestamp"} {
			props = append(props, fields{"name": name, "dataType": []string{"text"}, "tokenization": "field"})
		}
		props = append(props, fields{"name": "success", "dataType": []string{"boolean"}})
		schema = fields{"class": r.Collection, "vectorizer": "none", "properties": props}
	case "marqo":
		lookup = r.path + "/settings"
		create = r.path
		method = "POST"
		schema = fields{"type": "unstructured"}
		if r.MarqoModel != "" {
			schema["model"] = r.MarqoModel
		}
	case "milvus":
		return r.provisionMilvus(ctx)
	case "pinecone":
		return r.provisionPinecone(ctx)
	default:
		return fmt.Errorf("no provisioner for %s", r.Provider)
	}
	if err := r.call(ctx, "GET", lookup, nil, nil); err == nil {
		return nil
	} else if !errors.Is(err, errMissing) {
		return err
	}
	if err := r.call(ctx, method, create, schema, nil); err != nil {
		if again := r.call(ctx, "GET", lookup, nil, nil); again == nil {
			return nil
		}
		return err
	}
	return nil
}
func (r *remote) milvusCollection(ctx context.Context, action string, b fields) (fields, error) {
	b["collectionName"] = r.Collection
	if r.Database != "" {
		b["dbName"] = r.Database
	}
	var res struct {
		Code    int
		Message string
		Data    fields
	}
	if err := r.call(ctx, "POST", "/v2/vectordb/collections/"+action, b, &res); err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("Milvus %s (%d): %s", action, res.Code, res.Message)
	}
	return res.Data, nil
}
func (r *remote) provisionMilvus(ctx context.Context) error {
	exists, err := r.milvusCollection(ctx, "has", fields{})
	if err != nil {
		return err
	}
	if exists["has"] != true {
		attrs := []fields{{"fieldName": "id", "dataType": "VarChar", "isPrimary": true, "elementTypeParams": fields{"max_length": "100"}}, {"fieldName": "embedding", "dataType": "FloatVector", "elementTypeParams": fields{"dim": strconv.Itoa(r.Dimension)}}, {"fieldName": "success", "dataType": "Bool"}}
		for name, size := range map[string]string{"question": "2000", "tool_name": "200", "args_json": "5000", "timestamp": "50", "metadata_json": "5000"} {
			attrs = append(attrs, fields{"fieldName": name, "dataType": "VarChar", "elementTypeParams": fields{"max_length": size}})
		}
		_, err = r.milvusCollection(ctx, "create", fields{"schema": fields{"autoId": false, "enabledDynamicField": false, "fields": attrs}, "indexParams": []fields{{"fieldName": "embedding", "indexName": "embedding", "metricType": "IP", "indexConfig": fields{"index_type": "AUTOINDEX"}}}})
		if err != nil {
			exists, check := r.milvusCollection(ctx, "has", fields{})
			if check != nil || exists["has"] != true {
				return err
			}
		}
	}
	_, err = r.milvusCollection(ctx, "load", fields{})
	return err
}
func (r *remote) provisionPinecone(ctx context.Context) error {
	control := *r
	control.Endpoint = r.ControlEndpoint
	if control.Endpoint == "" {
		control.Endpoint = "https://api.pinecone.io"
	}
	control.Endpoint = strings.TrimRight(control.Endpoint, "/")
	path := "/indexes/" + url.PathEscape(r.Collection)
	var desc struct {
		Host   string
		Status struct{ Ready bool }
	}
	err := control.call(ctx, "GET", path, nil, &desc)
	if errors.Is(err, errMissing) {
		region := r.Region
		if region == "" {
			region = "us-east-1"
		}
		metric := r.Metric
		if metric == "" {
			metric = "cosine"
		}
		err = control.call(ctx, "POST", "/indexes", fields{"name": r.Collection, "dimension": r.Dimension, "metric": metric, "spec": fields{"serverless": fields{"cloud": "aws", "region": region}}}, nil)
		if err != nil {
			if check := control.call(ctx, "GET", path, nil, &desc); check != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for !desc.Status.Ready {
		if err := control.call(wait, "GET", path, nil, &desc); err != nil {
			return err
		}
		if !desc.Status.Ready {
			select {
			case <-wait.Done():
				return wait.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if desc.Host == "" {
		return errors.New("Pinecone index missing data host")
	}
	r.Endpoint = "https://" + desc.Host
	return nil
}
