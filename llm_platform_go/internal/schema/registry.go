// Package schema enforces request-body contracts. Each endpoint that accepts a
// JSON body has a JSON-Schema file (authored in YAML under requests/) that is
// embedded into the binary, compiled at startup, and used to reject malformed
// requests with a 422 before the handler ever decodes them. The YAML files are
// the single source of truth for the wire contract — editing one changes what
// the server accepts, no Go change required.
package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

//go:embed requests/*.yaml
var requestFS embed.FS

// Registry holds the compiled request schemas, keyed by name (the YAML file's
// base name without extension, e.g. "feedback").
type Registry struct {
	schemas map[string]*jsonschema.Schema
}

// MustLoadRequests compiles every requests/*.yaml schema, panicking on any
// failure. A broken contract file is a build/deploy error, not a runtime
// condition — failing fast at startup is the point.
func MustLoadRequests() *Registry {
	r, err := LoadRequests()
	if err != nil {
		panic("schema: " + err.Error())
	}
	return r
}

// LoadRequests compiles the embedded request schemas into a Registry.
func LoadRequests() (*Registry, error) {
	entries, err := fs.ReadDir(requestFS, "requests")
	if err != nil {
		return nil, fmt.Errorf("read embedded schemas: %w", err)
	}

	reg := &Registry{schemas: make(map[string]*jsonschema.Schema)}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := requestFS.ReadFile(path.Join("requests", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		s, err := compileYAML(name, raw)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", e.Name(), err)
		}
		reg.schemas[name] = s
	}
	if len(reg.schemas) == 0 {
		return nil, fmt.Errorf("no request schemas found under requests/")
	}
	return reg, nil
}

// Names returns the registered schema names, sorted — handy for diagnostics and
// for asserting full route coverage in tests.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.schemas))
	for k := range r.schemas {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a schema is registered under name.
func (r *Registry) Has(name string) bool {
	_, ok := r.schemas[name]
	return ok
}

// Validate checks a raw request body against the named schema. It returns an
// error describing the first problem (missing schema, empty/invalid JSON, or a
// schema violation); nil means the body conforms.
func (r *Registry) Validate(name string, body []byte) error {
	s, ok := r.schemas[name]
	if !ok {
		return fmt.Errorf("no schema registered for %q", name)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("request body is required")
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return s.Validate(inst)
}

// compileYAML converts a YAML JSON-Schema document to JSON and compiles it. We
// go through JSON so the schema is fed to the compiler in exactly the form the
// rest of the platform already uses (see tasks.compileSchema).
func compileYAML(name string, raw []byte) (*jsonschema.Schema, error) {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("yaml→json: %w", err)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	res := name + ".json"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(res, parsed); err != nil {
		return nil, fmt.Errorf("add resource: %w", err)
	}
	s, err := c.Compile(res)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return s, nil
}
