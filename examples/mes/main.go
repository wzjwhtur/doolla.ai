//go:build doolla_presto

// Command mes runs the Doolla CLI against a loopback Trino coordinator for two
// MES order counts. The database driver and model are the production adapters.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/prestodb/presto-go-client/presto"
	"doolla.ai/sdk/cli"
	_ "doolla.ai/sdk/integrations/database/drivers"
)

const clientName = "doolla-local-mes"

func loopbackAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	ip, ipErr := netip.ParseAddr(host)
	n, portErr := strconv.Atoi(port)
	return err == nil && ipErr == nil && ip.IsLoopback() && portErr == nil && n >= 0 && n <= 65535
}

func localEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !loopbackAddress(u.Host) ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") || u.Port() == "0" {
		return nil, errors.New("endpoint must be an HTTP(S) loopback IP and port without credentials, path, query, or fragment")
	}
	u.Path = ""
	return u, nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("doolla-mes", flag.ContinueOnError)
	flags.SetOutput(output)
	trino := flags.String("trino-url", "", "required loopback Trino coordinator, e.g. http://127.0.0.1:18080")
	listen := flags.String("listen", "127.0.0.1:8000", "loopback HTTP listen address")
	model := flags.String("model", "qwen2.5:1.5b", "installed local Ollama tool-calling model")
	modelURL := flags.String("model-url", "http://127.0.0.1:11434", "loopback Ollama base URL")
	dir := flags.String("workdir", ".doolla-mes", "local query artifact directory")
	history := flags.String("history-dir", "", "local conversation history directory (optional)")
	toolRounds := flags.Int("max-tool-iterations", 10, "maximum tool-call rounds per turn")
	audit := flags.Bool("audit", false, "write structured audit events")
	auditUI := flags.Bool("audit-ui", false, "include UI feature access checks in audit output")
	features := flags.String("ui-features", "", "JSON file mapping UI feature names to allowed groups")
	stream := flags.Bool("stream-model", true, "use the local model's streaming API")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !loopbackAddress(*listen) || strings.TrimSpace(*model) == "" {
		return errors.New("provide a loopback listen address, a model name, and no positional arguments")
	}
	origin, err := localEndpoint(*trino)
	if err != nil {
		return errors.Join(errors.New("invalid -trino-url"), err)
	}
	llm, err := localEndpoint(*modelURL)
	if err != nil {
		return errors.Join(errors.New("invalid -model-url"), err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     mesTransport{origin: origin, next: transport, logger: log.New(output, "", log.LstdFlags)},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	if err := presto.RegisterCustomClient(clientName, client); err != nil {
		return err
	}
	defer presto.DeregisterCustomClient(clientName)
	dsn := *origin
	dsn.User = url.User("doolla")
	dsn.RawQuery = url.Values{"catalog": {"mes"}, "schema": {"dbo"}, "custom_client": {clientName}}.Encode()
	return cli.Run(ctx, []string{
		"-listen", *listen, "-provider", "ollama", "-model", *model, "-model-url", llm.String(),
		"-database", "presto", "-dsn", dsn.String(), "-memory", "local", "-workdir", *dir,
		"-history-dir", *history, "-max-tool-iterations", strconv.Itoa(*toolRounds),
		"-audit=" + strconv.FormatBool(*audit), "-audit-ui=" + strconv.FormatBool(*auditUI),
		"-ui-features", *features, "-stream-model=" + strconv.FormatBool(*stream),
	}, output)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}
