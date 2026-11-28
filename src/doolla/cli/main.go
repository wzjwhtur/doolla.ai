// Package cli is the entry point shared by the SQLite and optional database builds.
package cli

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
	"doolla.ai/sdk/integrations/memory"

	"doolla.ai/sdk/internal/i18n"
)

type MemoryOpener func(context.Context, memory.Config) (doolla.MemoryStore, error)

func Main() { MainWithMemory(memory.Open) }

// MainWithMemory runs the same CLI with optional native memory engines.
func MainWithMemory(open MemoryOpener) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr, open); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Print(err)
		os.Exit(1)
	}
}

func Run(ctx context.Context, args []string, output io.Writer) error {
	return run(ctx, args, output, memory.Open)
}

func run(ctx context.Context, args []string, output io.Writer, open MemoryOpener) error {
	flags := flag.NewFlagSet("doolla", flag.ContinueOnError)
	flags.SetOutput(output)
	localeFlag := flags.String("locale", env("DOOLLA_LOCALE", "en"), i18n.Format(i18n.English, "cli.flag.locale"))
	configPath := flags.String("config", "", i18n.Format(i18n.English, "cli.flag.config"))
	addr := flags.String("listen", "127.0.0.1:8000", i18n.Format(i18n.English, "cli.flag.listen"))
	provider := flags.String("provider", env("DOOLLA_PROVIDER", "mock"), i18n.Format(i18n.English, "cli.flag.provider"))
	model := flags.String("model", os.Getenv("DOOLLA_MODEL"), i18n.Format(i18n.English, "cli.flag.model"))
	base := flags.String("model-url", os.Getenv("DOOLLA_MODEL_URL"), i18n.Format(i18n.English, "cli.flag.model-url"))
	backend := flags.String("database", env("DOOLLA_DATABASE", "sqlite"), i18n.Format(i18n.English, "cli.flag.database"))
	dsn := flags.String("dsn", os.Getenv("DOOLLA_DSN"), i18n.Format(i18n.English, "cli.flag.dsn"))
	duckInit := flags.String("duckdb-init-sql", os.Getenv("DOOLLA_DUCKDB_INIT_SQL"), i18n.Format(i18n.English, "cli.flag.duckdb-init-sql"))
	maxRows := flags.Int("sql-max-rows", doolla.DefaultSQLMaxRows, i18n.Format(i18n.English, "cli.flag.sql-max-rows"))
	maxBytes := flags.Int("sql-max-bytes", doolla.DefaultSQLMaxBytes, i18n.Format(i18n.English, "cli.flag.sql-max-bytes"))
	writable := flags.Bool("sqlite-writable", false, i18n.Format(i18n.English, "cli.flag.sqlite-writable"))
	verifyAnswers := flags.Bool("verify-tool-answers", true, i18n.Format(i18n.English, "cli.flag.verify-tool-answers"))
	stream := flags.Bool("stream-model", true, i18n.Format(i18n.English, "cli.flag.stream-model"))
	maxOutput := flags.Int("max-output-tokens", 0, i18n.Format(i18n.English, "cli.flag.max-output-tokens"))
	temperature := flags.String("temperature", os.Getenv("DOOLLA_TEMPERATURE"), i18n.Format(i18n.English, "cli.flag.temperature"))
	autoSave := flags.Bool("auto-save-conversations", true, i18n.Format(i18n.English, "cli.flag.auto-save-conversations"))
	showProgress := flags.Bool("show-progress", true, i18n.Format(i18n.English, "cli.flag.show-progress"))
	legacyMax := flags.Bool("openai-legacy-max-tokens", false, i18n.Format(i18n.English, "cli.flag.openai-legacy-max-tokens"))
	dir := flags.String("workdir", env("DOOLLA_WORKDIR", ".doolla"), i18n.Format(i18n.English, "cli.flag.workdir"))
	history := flags.String("history-dir", os.Getenv("DOOLLA_HISTORY_DIR"), i18n.Format(i18n.English, "cli.flag.history-dir"))
	var mem memoryOptions
	mem.register(flags)
	textRecall, toolRecall := doolla.DefaultTextRecall(), doolla.DefaultToolRecall()
	flags.IntVar(&textRecall.Limit, "memory-text-limit", textRecall.Limit, i18n.Format(i18n.English, "cli.flag.memory-text-limit"))
	flags.Float64Var(&textRecall.Threshold, "memory-text-threshold", textRecall.Threshold, i18n.Format(i18n.English, "cli.flag.memory-text-threshold"))
	flags.IntVar(&toolRecall.Limit, "memory-tool-limit", toolRecall.Limit, i18n.Format(i18n.English, "cli.flag.memory-tool-limit"))
	flags.Float64Var(&toolRecall.Threshold, "memory-tool-threshold", toolRecall.Threshold, i18n.Format(i18n.English, "cli.flag.memory-tool-threshold"))
	confirmSQL := flags.Bool("confirm-sql-memory", false, i18n.Format(i18n.English, "cli.flag.confirm-sql-memory"))
	flags.IntVar(&mem.config.EmbedCacheSize, "embed-cache-size", 0, i18n.Format(i18n.English, "cli.flag.embed-cache-size"))
	var embed embeddingOptions
	flags.StringVar(&embed.url, "embed-url", os.Getenv("DOOLLA_EMBED_URL"), i18n.Format(i18n.English, "cli.flag.embed-url"))
	flags.StringVar(&embed.model, "embed-model", os.Getenv("DOOLLA_EMBED_MODEL"), i18n.Format(i18n.English, "cli.flag.embed-model"))
	flags.StringVar(&embed.dimension, "embed-dim", os.Getenv("DOOLLA_EMBED_DIM"), i18n.Format(i18n.English, "cli.flag.embed-dim"))
	flags.StringVar(&embed.apiKey, "embed-api-key", "", i18n.Format(i18n.English, "cli.flag.embed-api-key"))
	// Apply the environment fallback without publishing the token in flag help.
	embed.apiKey = os.Getenv("DOOLLA_EMBED_API_KEY")
	toolRounds := flags.Int("max-tool-iterations", 10, i18n.Format(i18n.English, "cli.flag.max-tool-iterations"))
	origins := flags.String("allow-origins", os.Getenv("DOOLLA_ALLOWED_ORIGINS"), i18n.Format(i18n.English, "cli.flag.allow-origins"))
	audit := flags.Bool("audit", false, i18n.Format(i18n.English, "cli.flag.audit"))
	logTimings := flags.Bool("log-timings", false, i18n.Format(i18n.English, "cli.flag.log-timings"))
	var options doolla.ServerOptions
	flags.DurationVar(&options.Replay.Retention, "replay-retention", 0, i18n.Format(i18n.English, "cli.flag.replay-retention"))
	flags.IntVar(&options.Replay.MaxEntries, "replay-max-entries", 0, i18n.Format(i18n.English, "cli.flag.replay-max-entries"))
	flags.IntVar(&options.Replay.MaxInFlight, "replay-max-in-flight", 0, i18n.Format(i18n.English, "cli.flag.replay-max-in-flight"))
	flags.IntVar(&options.Replay.MaxBytes, "replay-max-bytes", 0, i18n.Format(i18n.English, "cli.flag.replay-max-bytes"))
	flags.IntVar(&options.Replay.MaxResponseBytes, "replay-max-response-bytes", 0, i18n.Format(i18n.English, "cli.flag.replay-max-response-bytes"))
	featureConfig := flags.String("ui-features", "", i18n.Format(i18n.English, "cli.flag.ui-features"))
	replaceFeatures := flags.Bool("replace-ui-features", false, i18n.Format(i18n.English, "cli.flag.replace-ui-features"))
	auditUI := flags.Bool("audit-ui", false, i18n.Format(i18n.English, "cli.flag.audit-ui"))
	auditText := flags.Bool("audit-response-text", false, i18n.Format(i18n.English, "cli.flag.audit-response-text"))
	flags.Usage = func() {
		if *configPath != "" {
			_ = applyConfig(flags, *configPath)
		}
		locale := i18n.Resolve(*localeFlag, "", "")
		flags.VisitAll(func(option *flag.Flag) {
			option.Usage = i18n.Format(locale, "cli.flag."+option.Name)
		})
		fmt.Fprintln(output, i18n.Format(locale, "cli.usage", flags.Name()))
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configPath != "" {
		if err := applyConfig(flags, *configPath); err != nil {
			return fmt.Errorf("read config %q: %w", *configPath, err)
		}
	}
	locale := i18n.Normalize(*localeFlag)
	if locale == "" {
		return errors.New(i18n.Format(i18n.Resolve(os.Getenv("DOOLLA_LOCALE"), "", ""), "cli.invalid_locale", *localeFlag))
	}
	ctx = i18n.WithLocale(ctx, locale)
	if r := options.Replay; r.Retention < 0 || r.MaxEntries < 0 || r.MaxInFlight < 0 || r.MaxBytes < 0 || r.MaxResponseBytes < 0 {
		return errors.New(i18n.T(ctx, "cli.error.replay_bounds"))
	}
	if *duckInit != "" && !strings.EqualFold(*backend, "duckdb") {
		return errors.New(i18n.T(ctx, "cli.error.duckdb_option"))
	}
	if flags.NArg() != 0 {
		return errors.New(i18n.T(ctx, "cli.error.unexpected_arg", strconv.Quote(flags.Arg(0))))
	}
	if err := applyTypedEnv(flags); err != nil {
		return err
	}
	if *maxRows <= 0 || *maxBytes <= 0 {
		return errors.New(i18n.T(ctx, "cli.error.sql_limits"))
	}
	if err := textRecall.Validate(); err != nil {
		return fmt.Errorf("%s: %w", i18n.T(ctx, "cli.error.text_memory"), err)
	}
	if err := toolRecall.Validate(); err != nil {
		return fmt.Errorf("%s: %w", i18n.T(ctx, "cli.error.tool_memory"), err)
	}
	// 空配置保留适配器默认值，显式的 0 是有效采样温度。
	var sampling *float64
	if *temperature != "" {
		value, err := strconv.ParseFloat(*temperature, 64)
		if err != nil {
			return fmt.Errorf("%s: %w", i18n.T(ctx, "cli.error.temperature"), err)
		}
		sampling = &value
	}
	llm, err := doolla.NewModel(doolla.ModelConfig{
		Provider: *provider, BaseURL: *base, Model: *model, APIKey: os.Getenv("DOOLLA_API_KEY"),
		Temperature: sampling, MaxOutputTokens: *maxOutput, OpenAIUseLegacyMaxTokens: *legacyMax,
	})
	if err != nil {
		return err
	}
	if mem.config.EmbedCacheSize < 0 {
		return errors.New(i18n.T(ctx, "cli.error.embed_cache_size"))
	}
	store, err := mem.open(ctx, flags, *dir, embed, open)
	if err != nil {
		return err
	}
	if closer, ok := store.(io.Closer); ok {
		defer closer.Close()
	}
	cfg := doolla.AgentConfig{DefaultLocale: locale, VerifyToolAnswers: *verifyAnswers, DirectTextRecall: true, DirectSQLResult: true, DisableAutoSave: !*autoSave, HideProgress: !*showProgress, Memory: store, ConfirmSQLMemory: *confirmSQL, TextMemoryRecall: textRecall, MaxToolIterations: *toolRounds, StreamResponses: *stream, ReplaceDefaultUIFeatures: *replaceFeatures,
		SystemPrompt: "You are a data analyst. Use the available tools to answer questions using actual results. Inspect the database schema when necessary. Use visualize_data for charts. With one query result in the current turn, omit its source argument. With multiple results, pass the appropriate result_id returned by run_sql. If asked to select an existing result_id, reuse it without rerunning SQL. Use filename only for an explicitly named existing CSV."}
	if *featureConfig != "" {
		data, err := os.ReadFile(*featureConfig)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &cfg.UIFeatures); err != nil {
			return fmt.Errorf("%s: %w", i18n.T(ctx, "cli.error.ui_groups"), err)
		}
		if cfg.UIFeatures == nil {
			return errors.New(i18n.T(ctx, "cli.error.feature_object"))
		}
		for name, groups := range cfg.UIFeatures {
			if strings.TrimSpace(name) == "" || groups == nil {
				return errors.New(i18n.T(ctx, "cli.error.feature_group"))
			}
		}
	}
	if *logTimings {
		cfg.Observability = timingLogger{logger: slog.New(slog.NewJSONHandler(output, nil))}
	}
	if *audit || *auditUI || *auditText {
		logger := slog.New(slog.NewJSONHandler(output, nil))
		cfg.Observe = doolla.LoggingObserver(logger)
		cfg.Audit = &doolla.AuditConfig{Sink: func(ctx context.Context, event doolla.AuditEvent) error {
			record := slog.NewRecord(event.Timestamp, slog.LevelInfo, "doolla.audit", 0)
			record.Add("event_type", event.Type, "audit", event)
			return logger.Handler().Handle(ctx, record)
		}, LogUIFeatureChecks: *auditUI, IncludeFullAIResponses: *auditText}
	}
	if *history != "" {
		cfg.Conversations = doolla.FileConversations{Directory: *history}
	}
	tools := doolla.MemoryToolsWithRecall(store, toolRecall)
	artifacts := doolla.Artifacts{Directory: filepath.Join(*dir, "files")}
	if *dsn != "" || strings.EqualFold(*backend, "duckdb") {
		var db *sql.DB
		if *backend == "sqlite" {
			cfg.SystemPrompt += " The database is SQLite. You can inspect sqlite_schema for table definitions."
			if *writable {
				db, err = doolla.OpenWritableSQLite(*dsn)
			} else {
				db, err = doolla.OpenSQLite(*dsn)
			}
		} else {
			if strings.EqualFold(*backend, "duckdb") {
				db, err = database.OpenDuckDB(*dsn, *duckInit)
			} else {
				db, err = database.Open(*backend, *dsn)
			}
			cfg.SystemPrompt += " The database dialect is " + *backend + "."
		}
		if err != nil {
			return err
		}
		defer db.Close()
		probe, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = db.PingContext(probe)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", i18n.T(ctx, "cli.error.database"), err)
		}
		tools = append(tools, doolla.Registration{Tool: doolla.SQLTool{DB: db, Artifacts: &artifacts, Limits: doolla.SQLResultLimits{MaxRows: *maxRows, MaxBytes: *maxBytes}}}, doolla.Registration{Tool: doolla.VisualizationTool{Artifacts: artifacts}})
		switch strings.ToLower(strings.TrimSpace(*backend)) {
		case "mssql", "sqlserver":
			tools = append(tools, doolla.Registration{Tool: database.MSSQLSchemaTool{DB: db}})
			database.ConfigureMSSQLRetry(&cfg)
			database.ConfigureMSSQLSchemaFirst(&cfg, db)
			// 表结构工具给出当前账号实际查到的范围，回答中的结论也应限定在这个范围内。
			cfg.SystemPrompt += `
For SQL Server data questions, inspect each new business table before using its columns, including lookup tables introduced midway to interpret status codes. Batch related tables with inspect_mssql_schema. Schema information contains no business rows: use the returned column names and SQL types to submit the data query and wait for its result before answering. When dictionary keys are unknown, sample the actual code and label columns with SELECT TOP before filtering for labels. If that unfiltered sample is empty, stop searching that table and report the original codes. For a trailing time window, use the same start and end across counts, samples and charts, ending today rather than at a future date.
For table or relationship questions, discover candidate schema and table names if needed. Use the inspector's columns, primary keys and incoming/outgoing foreign keys as catalog evidence. To expand the scope, inspect additional tables. Avoid repeating catalog lookups in run_sql. After an invalid-column error, inspect the affected table before retrying.
In the answer, name the inspected tables or state their scope. Distinguish declared foreign keys, joins with observed matching rows, and proposed relationships inferred from columns. Report zero matches or empty tables as unverified relationships. The catalog is scoped to the requested tables and the current account's visibility. When reporting a foreign-key count, qualify it in the same sentence or heading (e.g. “在本次检查的表范围内找到一条声明的外键”). If other_visible_foreign_keys_exist is true, the database has visible foreign keys outside that batch. Never describe a scoped result as the only foreign key in the database.`
		}
	}
	agent, err := doolla.NewConfiguredAgent(llm, cfg, tools...)
	if err != nil {
		return err
	}
	var resolver doolla.UserResolver
	if secret := os.Getenv("DOOLLA_SESSION_SECRET"); secret != "" {
		if len(secret) < 32 {
			return errors.New(i18n.T(ctx, "cli.error.session_length"))
		}
		resolver = doolla.SignedBrowserUsers([]byte(secret))
	}
	if token := os.Getenv("DOOLLA_ADMIN_TOKEN"); token != "" {
		fallback := resolver
		if fallback == nil {
			fallback = doolla.BrowserUsers()
		}
		resolver = func(w http.ResponseWriter, r *http.Request) (doolla.User, error) {
			header := r.Header.Get("Authorization")
			if header == "" {
				return fallback(w, r)
			}
			bearer, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) != 1 {
				return doolla.User{}, errors.New("invalid bearer token")
			}
			return doolla.User{ID: "doolla-admin", Groups: []string{"admin", "user"}}, nil
		}
	}
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			options.AllowedOrigins = append(options.AllowedOrigins, o)
		}
	}
	srv := http.Server{Handler: doolla.NewServerWithOptions(agent, resolver, options), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, BaseContext: func(net.Listener) context.Context { return ctx }}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, i18n.T(ctx, "cli.listening", listener.Addr()))
	fmt.Fprintln(output, i18n.T(ctx, "cli.memory_embedding", mem.config.Provider, embeddingMode(mem.config.Provider, embed.url), embed.model))
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Serve(listener) }()
	select {
	case err = <-stopped:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return err
		}
		err = <-stopped
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func env(k, fallback string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return fallback
}

type embeddingOptions struct {
	url, model, dimension, apiKey string
}

type memoryOptions struct {
	config                         memory.Config
	faissDir, qdrantDir, chromaDir string
}

func (opts *memoryOptions) register(flags *flag.FlagSet) {
	c := &opts.config
	flags.StringVar(&c.Provider, "memory", env("DOOLLA_MEMORY", "local"), i18n.Format(i18n.English, "cli.flag.memory"))
	flags.StringVar(&c.Endpoint, "memory-url", os.Getenv("DOOLLA_MEMORY_URL"), i18n.Format(i18n.English, "cli.flag.memory-url"))
	flags.StringVar(&c.Collection, "memory-collection", os.Getenv("DOOLLA_MEMORY_COLLECTION"), i18n.Format(i18n.English, "cli.flag.memory-collection"))
	flags.StringVar(&c.Database, "memory-database", os.Getenv("DOOLLA_MEMORY_DATABASE"), i18n.Format(i18n.English, "cli.flag.memory-database"))
	flags.StringVar(&c.Tenant, "memory-tenant", os.Getenv("DOOLLA_MEMORY_TENANT"), i18n.Format(i18n.English, "cli.flag.memory-tenant"))
	flags.StringVar(&c.Namespace, "memory-namespace", os.Getenv("DOOLLA_MEMORY_NAMESPACE"), i18n.Format(i18n.English, "cli.flag.memory-namespace"))
	flags.StringVar(&c.Organization, "memory-organization", os.Getenv("DOOLLA_MEMORY_ORGANIZATION"), i18n.Format(i18n.English, "cli.flag.memory-organization"))
	flags.StringVar(&c.MarqoModel, "memory-marqo-model", os.Getenv("DOOLLA_MEMORY_MARQO_MODEL"), i18n.Format(i18n.English, "cli.flag.memory-marqo-model"))
	flags.StringVar(&c.ControlEndpoint, "memory-control-url", os.Getenv("DOOLLA_MEMORY_CONTROL_URL"), i18n.Format(i18n.English, "cli.flag.memory-control-url"))
	flags.StringVar(&c.Region, "memory-region", os.Getenv("DOOLLA_MEMORY_REGION"), i18n.Format(i18n.English, "cli.flag.memory-region"))
	flags.StringVar(&c.APIKey, "memory-api-key", "", i18n.Format(i18n.English, "cli.flag.memory-api-key"))
	c.APIKey = os.Getenv("DOOLLA_MEMORY_API_KEY")
	flags.BoolVar(&c.Provision, "memory-provision", false, i18n.Format(i18n.English, "cli.flag.memory-provision"))
	flags.StringVar(&opts.faissDir, "faiss-dir", os.Getenv("DOOLLA_FAISS_DIR"), i18n.Format(i18n.English, "cli.flag.faiss-dir"))
	flags.StringVar(&c.Metric, "faiss-metric", os.Getenv("DOOLLA_FAISS_METRIC"), i18n.Format(i18n.English, "cli.flag.faiss-metric"))
	flags.StringVar(&opts.qdrantDir, "qdrant-dir", os.Getenv("DOOLLA_QDRANT_DIR"), i18n.Format(i18n.English, "cli.flag.qdrant-dir"))
	flags.StringVar(&opts.chromaDir, "chroma-dir", os.Getenv("DOOLLA_CHROMA_DIR"), i18n.Format(i18n.English, "cli.flag.chroma-dir"))
}

func (opts memoryOptions) open(ctx context.Context, flags *flag.FlagSet, dir string, embed embeddingOptions, open MemoryOpener) (doolla.MemoryStore, error) {
	config := opts.config
	if config.Provider == "local" {
		return doolla.NewLocalMemory(10000), nil
	}
	switch config.Provider {
	case "faiss":
		config.Directory = opts.faissDir
		if config.Directory == "" {
			config.Directory = filepath.Join(dir, "faiss")
		}
	case "qdrant":
		config.Directory = opts.qdrantDir
	case "chroma", "chromadb":
		config.Directory = opts.chromaDir
	}
	if value := os.Getenv("DOOLLA_MEMORY_PROVISION"); value != "" {
		// Validate the environment fallback only if it will actually be used.
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "memory-provision" {
				value = ""
			}
		})
		if value != "" {
			provision, err := strconv.ParseBool(value)
			if err != nil {
				return nil, errors.New(i18n.T(ctx, "cli.error.provision"))
			}
			config.Provision = provision
		}
	}
	if embed.dimension != "" {
		n, err := strconv.Atoi(embed.dimension)
		if err != nil || n < 1 {
			return nil, errors.New(i18n.T(ctx, "cli.error.embed_dim"))
		}
		config.Dimension = n
	}
	if embed.url != "" {
		if embed.model == "" {
			return nil, errors.New(i18n.T(ctx, "cli.error.embed_model"))
		}
		config.Embed = embeddingClient(embed.url, embed.model, embed.apiKey)
	}
	return open(ctx, config)
}
func embeddingClient(base, model, key string) func(context.Context, string) ([]float32, error) {
	client := http.Client{Timeout: time.Minute}
	return func(ctx context.Context, input string) ([]float32, error) {
		b, err := json.Marshal(map[string]string{"input": input, "model": model})
		if err != nil {
			return nil, err
		}
		r, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/embeddings", bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := client.Do(r)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("embeddings returned HTTP %d", resp.StatusCode)
		}
		var data struct {
			Data []struct{ Embedding []float32 }
		}
		if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}
		if len(data.Data) != 1 || len(data.Data[0].Embedding) == 0 {
			return nil, errors.New("expected one embedding vector")
		}
		return data.Data[0].Embedding, nil
	}
}

// Announce the effective indexing strategy without exposing the endpoint or key.
func embeddingMode(provider, embedURL string) string {
	switch provider {
	case "local":
		return "lexical"
	case "marqo", "premium":
		return "backend-managed"
	}
	if embedURL != "" {
		return "external"
	}
	if provider == "chroma" || provider == "chromadb" {
		return "native-model"
	}
	return "legacy-md5"
}
