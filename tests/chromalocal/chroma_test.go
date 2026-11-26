package chromalocal_test

import (
	"io"
	"os"
	"os/exec"
	"testing"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/chromalocal"
	"doolla.ai/sdk/integrations/memory"
)

func TestConflictingLocations(t *testing.T) {
	_, err := chromalocal.Open(t.Context(), memory.Config{Provider: "chroma", Directory: t.TempDir(), Endpoint: "http://localhost:8000"})
	if err == nil {
		t.Fatal("expected mutually exclusive Chroma locations")
	}
}

func TestNativeChroma(t *testing.T) {
	if os.Getenv("DOOLLA_TEST_NATIVE_CHROMA") != "1" {
		t.Skip("requires native Chroma and ONNX libraries")
	}
	dir := t.TempDir()
	python := os.Getenv("DOOLLA_TEST_CHROMA_PYTHON")
	runPython := func(code string) {
		t.Helper()
		if output, err := exec.CommandContext(t.Context(), python, "-c", "import chromadb,sys\nc=chromadb.PersistentClient(path=sys.argv[1]).get_or_create_collection('tool_memories')\n"+code, dir).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, output)
		}
	}
	if python != "" {
		runPython("c.add(ids=['legacy'],documents=['Water usage per factory'],metadatas=[{'content':'Water usage per factory','is_text_memory':True}])")
	}
	open := func() doolla.MemoryStore {
		t.Helper()
		s, err := chromalocal.Open(t.Context(), memory.Config{Provider: "chroma", Directory: dir})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	if err := s.Save(t.Context(), doolla.MemoryEntry{Kind: "text", ID: "sample", Content: "Net revenue excludes refunds"}); err != nil {
		t.Fatal(err)
	}
	if err := s.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	s = open()

	matches, err := s.Search(t.Context(), doolla.MemoryQuery{Kind: "text", Text: "Net revenue excludes refunds", Limit: 1, Threshold: .95})
	if err != nil || len(matches) != 1 || matches[0].Memory.ID != "sample" {
		t.Fatal(matches, err)
	}
	if python != "" {
		matches, err = s.Search(t.Context(), doolla.MemoryQuery{Kind: "text", Text: "Water usage per factory", Limit: 1, Threshold: .98})
		if err != nil || len(matches) != 1 || matches[0].Memory.ID != "legacy" {
			t.Fatal(matches, err)
		}
	}
	if err := s.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	if python != "" {
		runPython("r=c.query(query_texts=['Net revenue excludes refunds'],n_results=1); assert r['ids']==[['sample']] and r['distances'][0][0]<0.02,r")
	}
}
