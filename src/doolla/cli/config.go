package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func applyTypedEnv(flags *flag.FlagSet) error {
	supplied := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	for _, name := range []string{"embed-cache-size", "confirm-sql-memory", "sql-max-rows", "sql-max-bytes", "memory-text-limit", "memory-text-threshold", "memory-tool-limit", "memory-tool-threshold"} {
		if supplied[name] {
			continue
		}
		key := "DOOLLA_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if value := os.Getenv(key); value != "" {
			if err := flags.Set(name, value); err != nil {
				return fmt.Errorf("invalid %s: %w", key, err)
			}
		}
	}
	return nil
}

func applyConfig(flags *flag.FlagSet, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("expected a YAML or JSON object with startup options")
	}
	if err := decoder.Decode(&yaml.Node{}); err != io.EOF {
		return errors.New("expected a single configuration document")
	}

	// Keep explicit CLI values, including false, zero and the empty string.
	explicit := make(map[string]string)
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = f.Value.String() })
	entries := doc.Content[0].Content
	seen := make(map[string]bool)
	for i := 0; i < len(entries); i += 2 {
		key, value := entries[i], entries[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return fmt.Errorf("expected an option name at line %d", key.Line)
		}
		name := key.Value
		option := flags.Lookup(name)
		if option == nil || name == "config" {
			return fmt.Errorf("unsupported option %q at line %d", name, key.Line)
		}
		if seen[name] {
			return fmt.Errorf("duplicate option %q at line %d", name, key.Line)
		}
		seen[name] = true
		if value.Kind != yaml.ScalarNode || value.ShortTag() == "!!null" {
			return fmt.Errorf("expected a scalar value for %q at line %d", name, value.Line)
		}
		if err := flags.Set(name, value.Value); err != nil {
			return fmt.Errorf("invalid value for %q at line %d", name, value.Line)
		}
		if override, ok := explicit[name]; ok {
			if err := option.Value.Set(override); err != nil {
				return fmt.Errorf("invalid command-line override for %q", name)
			}
		}
	}
	return nil
}
