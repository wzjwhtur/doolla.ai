package chromalocal_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/chromalocal"
	"doolla.ai/sdk/integrations/memory"
)

func TestNativeChromaOllamaAgent(t *testing.T) {
	endpoint := os.Getenv("DOOLLA_TEST_OLLAMA_URL")
	if os.Getenv("DOOLLA_TEST_NATIVE_CHROMA") != "1" || endpoint == "" {
		t.Skip("set DOOLLA_TEST_NATIVE_CHROMA=1 and DOOLLA_TEST_OLLAMA_URL to run native Chroma and Ollama")
	}
	modelName := os.Getenv("DOOLLA_TEST_OLLAMA_MODEL")
	if modelName == "" {
		modelName = "qwen2.5:1.5b"
	}
	ctx := t.Context()
	store, err := chromalocal.Open(ctx, memory.Config{Provider: "chroma", Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.(io.Closer).Close(); err != nil {
			t.Error(err)
		}
	})
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	code := "CL-" + strings.ToUpper(hex.EncodeToString(nonce[:]))
	fact := "The internal acceptance code for Cedar Mill Factory's offline memory check is " + code + "."
	question := "What is the internal acceptance code for Cedar Mill Factory's offline memory check?"
	if err := store.Save(ctx, doolla.MemoryEntry{Kind: "text", ID: "cedar-mill-code", Content: fact}); err != nil {
		t.Fatal(err)
	}
	found, err := store.Search(ctx, doolla.MemoryQuery{Kind: "text", Text: question, Limit: 5, Threshold: .7})
	if err != nil || len(found) != 1 || found[0].Memory.Content != fact {
		t.Fatalf("native semantic retrieval: matches=%v err=%v", found, err)
	}
	temperature := 0.0
	model, err := doolla.NewModel(doolla.ModelConfig{Provider: "ollama", BaseURL: endpoint, Model: modelName, Temperature: &temperature, MaxOutputTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	inspect := func(ctx context.Context, request doolla.ModelRequest, send func(context.Context, doolla.ModelRequest) (doolla.Message, error)) (doolla.Message, error) {
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || !strings.Contains(request.Messages[0].Content, fact) || request.Messages[1].Content != question {
			t.Fatal("agent must supply the retrieved fact in system context and preserve the original user question")
		}
		return send(ctx, request)
	}
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Memory: store, SystemPrompt: "Use the factory record in context. Reply with the exact code.", Middleware: []doolla.Middleware{inspect}})
	if err != nil {
		t.Fatal(err)
	}
	var answer string
	err = agent.Chat(ctx, doolla.User{ID: "acceptance"}, "new-session", question, func(component doolla.Component) error {
		if component.Type == "text" {
			answer, _ = component.Data["content"].(string)
		}
		return nil
	})
	if err != nil || !strings.Contains(answer, code) {
		t.Fatalf("expected newly generated code %s in answer %q: %v", code, answer, err)
	}
	t.Logf("model=%s retrieval_score=%.3f question=%q answer=%q code=%s", modelName, found[0].Score, question, answer, code)
}
