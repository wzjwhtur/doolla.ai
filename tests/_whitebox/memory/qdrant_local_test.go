//go:build cgo

package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
)

const qdrantTestID = "e8eb0fea-9931-4adb-ab7d-b957c468923d"

func TestQdrantLocalLifecycle(t *testing.T) {
	ctx := t.Context()
	for _, directory := range []string{":memory:", t.TempDir()} {
		disk := directory != ":memory:"
		c := Config{Provider: "qdrant", Directory: directory, Dimension: 4}
		s, err := Open(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if disk {
			if second, err := Open(ctx, c); err == nil {
				second.(io.Closer).Close()
				t.Fatal("opened locked directory")
			}
		}
		for _, id := range []string{qdrantTestID, "18446744073709551615"} {
			e := entry{ID: id, Kind: "tool", ToolName: "run_sql", Question: "total", Success: true, Args: fields{"sql": "select 42"}}
			if err := s.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
			e.Args["sql"] = "select 43"
			if err := s.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Save(ctx, entry{Kind: "text", Content: "schema"}); err != nil {
			t.Fatal(err)
		}
		result, err := s.Search(ctx, query{Kind: "tool", ToolName: "run_sql", Text: "total", Limit: 2, Threshold: .99})
		if err != nil || len(result) != 2 || result[0].Memory.Args["sql"] != "select 43" || math.Abs(result[0].Score-1) > 1e-6 {
			t.Fatal(result, err)
		}
		if removed, err := s.Delete(ctx, "tool", strings.ToUpper(qdrantTestID)); err != nil || !removed {
			t.Fatal(removed, err)
		}
		if err := s.(io.Closer).Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		mem, err := s.Recent(ctx, "tool", 10)
		if err != nil || disk && (len(mem) != 1 || mem[0].ID != "18446744073709551615") || !disk && len(mem) != 0 {
			t.Fatal(mem, err)
		}
		n, err := s.Clear(ctx, "", time.Time{})
		if err != nil || disk && n != 2 || !disk && n != 0 {
			t.Fatal(n, err)
		}
		if err := s.(io.Closer).Close(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestQdrantUUIDWithOnlyDigits(t *testing.T) {
	id, _, err := qdrantPointID("00000000000000000000000000000001")
	if err != nil || id != "00000000-0000-0000-0000-000000000001" {
		t.Fatal(id, err)
	}
}

func TestPointPickleRejectsUnknownClasses(t *testing.T) {
	b, err := encodeQdrantPoint(qdrantTestID, []float32{1}, fields{"question": "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDictionary(bytes.Replace(b, []byte("PointStruct"), []byte("WrongStruct"), 1), true); err == nil {
		t.Fatal("accepted unrelated Python class")
	}
	if _, err := decodePickle(b); err == nil {
		t.Fatal("FAISS accepted a Qdrant envelope")
	}
}

func TestQdrantPythonInteroperability(t *testing.T) {
	python := os.Getenv("DOOLLA_TEST_QDRANT_PYTHON")
	if python == "" {
		t.Skip("set DOOLLA_TEST_QDRANT_PYTHON to a Python executable with qdrant-client")
	}
	ctx := t.Context()
	dir := t.TempDir()
	run := func(code string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, python, "-c", code, dir).CombinedOutput(); err != nil {
			t.Fatalf("Python Qdrant: %s: %v", output, err)
		}
	}
	run(`
import base64, pathlib, pickle, sqlite3, sys
from qdrant_client import QdrantClient, models
p=sys.argv[1]; c=QdrantClient(path=p)
c.create_collection("tool_memories",vectors_config=models.VectorParams(size=2,distance=models.Distance.COSINE))
c.create_collection("untouched",vectors_config=models.VectorParams(size=4,distance=models.Distance.DOT))
c.update_collection_aliases([models.CreateAliasOperation(create_alias=models.CreateAlias(collection_name="tool_memories",alias_name="old_name"))])
id="6d673480-f4f7-4898-bba0-650bd17a4481"
point=models.PointStruct(id=id,vector=[1.,0.],payload={"question":"python","tool_name":"sql","success":True,"args":{"names":["old","new"]},"timestamp":"2026-01-01T00:00:00","metadata":{}})
c.upsert("tool_memories",points=[point]);c.close()
# Earlier clients could persist a second spelling of a point ID under a different pickle key.
db=sqlite3.connect(pathlib.Path(p,"collection","tool_memories","storage.sqlite"))
point.payload["question"]="latest Python record"
db.execute("INSERT INTO points VALUES (?,?)",(base64.b64encode(pickle.dumps(id.upper(),protocol=5)).decode(),pickle.dumps(point,protocol=5)))
db.commit();db.close()
`)
	c := Config{Provider: "qdrant", Directory: dir, Collection: "old_name", Dimension: 2, Embed: func(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }}
	s, err := Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	closer := s.(io.Closer)
	t.Cleanup(func() { closer.Close() })
	mem, err := s.Search(ctx, query{Kind: "tool", ToolName: "sql", Text: "python", Limit: 5, Threshold: .99})
	if err != nil || len(mem) != 1 || mem[0].Memory.Question != "latest Python record" {
		t.Fatal(mem, err)
	}
	labels, ok := mem[0].Memory.Args["names"].([]any)
	if !ok || len(labels) != 2 || labels[1] != "new" {
		t.Fatal(mem)
	}
	run(`
import sys
from qdrant_client import QdrantClient
try: c=QdrantClient(path=sys.argv[1])
except RuntimeError as e: assert "accessed by another instance" in str(e)
else: c.close(); raise AssertionError("expected exclusive directory lock")
`)
	e := mem[0].Memory
	e.Question = "Go updated"
	if err := s.Save(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, doolla.MemoryEntry{Kind: "text", ID: qdrantTestID, Content: "Go schema"}); err != nil {
		t.Fatal(err)
	}
	if err := s.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	// Adding a collection must preserve all configuration owned by Python.
	c.Collection = "from_go"
	other, err := Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Save(ctx, entry{Kind: "text", ID: qdrantTestID, Content: "new collection"}); err != nil {
		t.Fatal(err)
	}
	if err := other.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	run(`
import pathlib, sqlite3, sys
from qdrant_client import QdrantClient
p=sys.argv[1]
db=sqlite3.connect(pathlib.Path(p,"collection","tool_memories","storage.sqlite"))
assert db.execute("SELECT COUNT(*) FROM points").fetchone()==(2,)
db.close()
client=QdrantClient(path=p)
assert client.get_collection("untouched").config.params.vectors.size == 4
assert client.retrieve("old_name",ids=["6d673480-f4f7-4898-bba0-650bd17a4481"])[0].payload["question"]=="Go updated"
id="e8eb0fea-9931-4adb-ab7d-b957c468923d"
assert client.retrieve("tool_memories",ids=[id])[0].payload["content"]=="Go schema"
assert client.query_points("from_go",query=[1.,0.],limit=1).points[0].payload["content"]=="new collection"
client.delete("tool_memories",[id]);client.close()
`)
	c.Collection = "tool_memories"
	s, err = Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	memText, err := s.Recent(ctx, "text", 10)
	if err != nil || len(memText) != 0 {
		t.Fatal(memText, err)
	}
	if err := s.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil || !json.Valid(raw) {
		t.Fatal("invalid meta.json", err)
	}
}
