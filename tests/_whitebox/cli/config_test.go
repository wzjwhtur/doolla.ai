package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/memory"
)

func configFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEmbeddingConfig(t *testing.T) {
	for _, source := range []string{"env", "file", "cli"} {
		t.Run(source, func(t *testing.T) {
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/"+source+"/embeddings" || r.Header.Get("Authorization") != "Bearer "+source {
					t.Error("incorrect embedding endpoint or credential")
				}
				var req struct{ Model, Input string }
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != source || req.Input != "sample" {
					t.Error("incorrect embedding request")
				}
				io.WriteString(w, `{"data":[{"embedding":[1,0]}]}`)
			}))
			defer service.Close()
			t.Setenv("DOOLLA_EMBED_URL", service.URL+"/env")
			t.Setenv("DOOLLA_EMBED_MODEL", "env")
			t.Setenv("DOOLLA_EMBED_API_KEY", "env")
			t.Setenv("DOOLLA_EMBED_DIM", "2")
			args := []string{"-memory", "milvus", "-provider", "mock"}
			wantDim := 2
			if source != "env" {
				path := configFile(t, fmt.Sprintf("embed-url: %s/file\nembed-model: file\nembed-api-key: file\nembed-dim: 3\n", service.URL))
				args = append(args, "-config", path)
				wantDim = 3
			}
			if source == "cli" {
				args = append(args, "-embed-url", service.URL+"/cli", "-embed-api-key", "cli", "-embed-model", "cli", "-embed-dim", "4")
				wantDim = 4
			}
			stop := errors.New("verified memory options")
			err := run(t.Context(), args, io.Discard, func(ctx context.Context, cfg memory.Config) (doolla.MemoryStore, error) {
				if cfg.Dimension != wantDim || cfg.Embed == nil {
					t.Fatal("incorrect dimension or missing embedding client")
				}
				if _, err := cfg.Embed(ctx, "sample"); err != nil {
					t.Fatal(err)
				}
				return nil, stop
			})
			if !errors.Is(err, stop) {
				t.Fatal(err)
			}
		})
	}
}

func TestMemoryKeysHiddenInHelp(t *testing.T) {
	t.Setenv("DOOLLA_EMBED_API_KEY", "private-example-key")
	t.Setenv("DOOLLA_MEMORY_API_KEY", "memory-example-key")
	var output bytes.Buffer
	if err := Run(t.Context(), []string{"-help"}, &output); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-example-key") || strings.Contains(output.String(), "memory-example-key") {
		t.Fatal("help contains a credential")
	}
}

func TestMemoryConfigPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, envProvision, file, cli string
		wantProvision                 bool
		wantURL                       string
	}{
		{"environment only", "true", "", "", true, "env-url"},
		{"YAML false overrides environment", "true", "false", "", false, "file-url"},
		{"CLI false overrides YAML", "true", "true", "-memory-provision=false", false, "cli-url"},
		{"bare CLI flag overrides invalid environment", "bad", "false", "-memory-provision", true, "cli-url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOOLLA_MEMORY_URL", "env-url")
			t.Setenv("DOOLLA_MEMORY_PROVISION", tc.envProvision)
			t.Setenv("DOOLLA_MEMORY_DATABASE", "env-db")
			args := []string{"-memory", "milvus", "-provider", "mock"}
			if tc.file != "" {
				path := configFile(t, "memory-url: file-url\nmemory-collection: file-collection\nmemory-api-key: file-key\nmemory-provision: "+tc.file)
				args = append(args, "-config", path)
			}
			if tc.cli != "" {
				args = append(args, tc.cli, "-memory-url", "cli-url")
			}
			stop := errors.New("checked memory config")
			err := run(t.Context(), args, io.Discard, func(ctx context.Context, cfg memory.Config) (doolla.MemoryStore, error) {
				if cfg.Provision != tc.wantProvision || cfg.Endpoint != tc.wantURL || cfg.Database != "env-db" {
					t.Fatal("incorrect resolved memory configuration")
				}
				if tc.file != "" && (cfg.APIKey != "file-key" || cfg.Collection != "file-collection") {
					t.Fatal("incorrect file credential or collection")
				}
				return nil, stop
			})
			if !errors.Is(err, stop) {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigPrecedence(t *testing.T) {
	for _, content := range []string{
		"provider: ollama\naudit: true\nmax-output-tokens: 42\ndsn: database.sqlite\n",
		`{"provider":"ollama","audit":true,"max-output-tokens":42,"dsn":"database.sqlite"}`,
	} {
		flags := flag.NewFlagSet("doolla", flag.ContinueOnError)
		provider := flags.String("provider", "mock", "")
		audit := flags.Bool("audit", false, "")
		tokens := flags.Int("max-output-tokens", 10, "")
		dsn := flags.String("dsn", "env-dsn", "")
		model := flags.String("model", "env-model", "")
		if err := flags.Parse([]string{"-dsn=", "-audit=false", "-max-output-tokens=0"}); err != nil {
			t.Fatal(err)
		}
		if err := applyConfig(flags, configFile(t, content)); err != nil {
			t.Fatal(err)
		}
		if *provider != "ollama" || *audit || *tokens != 0 || *dsn != "" || *model != "env-model" {
			t.Fatal("incorrect option precedence")
		}
	}
}

func TestConfigErrors(t *testing.T) {
	for _, content := range []string{
		"", "[", "[audit]", "audit: false\n---\naudit: true", "audit: null",
		"audit: true\naudit: false", "unknown: secret-value", "config: nested.yaml",
		"audit: [true]", "audit: secret-value",
	} {
		flags := flag.NewFlagSet("doolla", flag.ContinueOnError)
		flags.Bool("audit", false, "")
		flags.String("config", "", "")
		err := applyConfig(flags, configFile(t, content))
		if err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Errorf("unexpected result for %q: %v", content, err)
		}
	}
}

func TestRecallConfiguration(t *testing.T) {
	t.Setenv("DOOLLA_MEMORY_TEXT_LIMIT", "invalid")
	t.Setenv("DOOLLA_MEMORY_TOOL_THRESHOLD", "0.9")
	flags := flag.NewFlagSet("doolla", flag.ContinueOnError)
	textLimit := flags.Int("memory-text-limit", 5, "")
	flags.Int("memory-tool-limit", 10, "")
	textThreshold := flags.Float64("memory-text-threshold", 0.7, "")
	toolThreshold := flags.Float64("memory-tool-threshold", 0.7, "")
	if err := flags.Parse([]string{"-memory-text-threshold=0"}); err != nil {
		t.Fatal(err)
	}
	if err := applyConfig(flags, configFile(t, "memory-text-limit: 3\nmemory-text-threshold: 0.8")); err != nil {
		t.Fatal(err)
	}
	if err := applyRecallEnv(flags); err != nil {
		t.Fatal(err)
	}
	if *textLimit != 3 || *textThreshold != 0 || *toolThreshold != 0.9 {
		t.Fatal("incorrect recall option precedence")
	}
	for _, args := range [][]string{
		{}, {"-memory-text-limit=0"}, {"-memory-text-limit=1", "-memory-tool-threshold=NaN"},
		{"-memory-text-limit=1", "-memory-text-threshold=1.1"}, {"-memory-text-limit=1", "-memory-tool-limit=-1"},
	} {
		if err := Run(t.Context(), args, io.Discard); err == nil {
			t.Fatalf("accepted invalid recall configuration %v", args)
		}
	}
}

func TestSamplingTemperatureConfig(t *testing.T) {
	t.Setenv("DOOLLA_TEMPERATURE", "invalid")
	base := []string{"-provider", "mock", "-memory", "milvus", "-workdir", t.TempDir()}
	check := func(args []string, expectError bool) {
		t.Helper()
		stop := errors.New("model configured")
		err := run(t.Context(), append(append([]string{}, base...), args...), io.Discard, func(context.Context, memory.Config) (doolla.MemoryStore, error) {
			return nil, stop
		})
		if expectError && (err == nil || !strings.Contains(err.Error(), "temperature")) || !expectError && !errors.Is(err, stop) {
			t.Fatal(err)
		}
	}
	check(nil, true)
	path := configFile(t, "temperature: 0.5\nshow-progress: false\nauto-save-conversations: false\n")
	check([]string{"-config", path}, false)
	check([]string{"-config", path, "-temperature", "0"}, false)
	check([]string{"-config", path, "-temperature", "2.1"}, true)
}
