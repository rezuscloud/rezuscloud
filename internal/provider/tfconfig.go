package provider

import "encoding/json"

// TFConfig is a builder for `.tf.json` documents using the canonical JSON-config
// object form. It is shared by every provider module (oci, openstack, metal, …)
// so they all emit structurally-valid OpenTofu/Terraform JSON with stable,
// diff-friendly output.
//
// Canonical `.tf.json` structure (each top-level key maps to an object keyed by
// the block's label(s)):
//
//	{
//	  "terraform": { "required_providers": { "oci": { "source": "oracle/oci" } } },
//	  "provider":  { "oci": { "region": "..." } },
//	  "resource":  { "TYPE": { "NAME": { ...body... } } },
//	  "data":      { "TYPE": { "NAME": { ...body... } } }
//	}
//
// `required_providers` is a single OBJECT mapping provider name → {source,version};
// emitting it as an array of single-key objects produces "Duplicate required
// providers configuration" at validate time. `resource`/`data` map TYPE → a
// NAME-keyed object of bodies (so multiple resources of one type merge).
type TFConfig struct {
	terraform Obj // single terraform block body (required_providers, backend, …)
	providers Obj // provider local-name → config
	resources Obj // TYPE → (NAME → body)
	dataSrcs  Obj // TYPE → (NAME → body)
}

// Obj is a JSON object. encoding/json sorts map keys alphabetically on marshal,
// which gives stable, diff-friendly output (no manual ordering needed).
type Obj = map[string]interface{}

// ReqProvider declares a required_provider entry for the terraform block.
type ReqProvider struct {
	Name    string
	Source  string
	Version string // optional; "" ⇒ omit
}

// NewTFConfig returns an empty config builder.
func NewTFConfig() TFConfig {
	return TFConfig{
		terraform: Obj{},
		providers: Obj{},
		resources: Obj{},
		dataSrcs:  Obj{},
	}
}

// AddRequiredProviders merges all given providers into a single
// `required_providers` object under the terraform block. Idempotent: calling
// twice with the same name overwrites the entry.
func (c *TFConfig) AddRequiredProviders(ps ...ReqProvider) {
	rps, ok := c.terraform["required_providers"].(Obj)
	if !ok {
		rps = Obj{}
		c.terraform["required_providers"] = rps
	}
	for _, p := range ps {
		entry := Obj{"source": p.Source}
		if p.Version != "" {
			entry["version"] = p.Version
		}
		rps[p.Name] = entry
	}
}

// AddProvider sets the config for a provider local name.
func (c *TFConfig) AddProvider(name string, body Obj) {
	c.providers[name] = body
}

// AddResource merges a resource of the given TYPE/NAME into the config. If a
// resource of the same TYPE already exists, NAME is merged into its object.
func (c *TFConfig) AddResource(typ, name string, body Obj) {
	byName, ok := c.resources[typ].(Obj)
	if !ok {
		byName = Obj{}
		c.resources[typ] = byName
	}
	byName[name] = body
}

// AddDataSource merges a data source of the given TYPE/NAME into the config.
func (c *TFConfig) AddDataSource(typ, name string, body Obj) {
	byName, ok := c.dataSrcs[typ].(Obj)
	if !ok {
		byName = Obj{}
		c.dataSrcs[typ] = byName
	}
	byName[name] = body
}

// MarshalJSON renders the config, omitting empty top-level sections.
func (c TFConfig) MarshalJSON() ([]byte, error) {
	out := Obj{}
	if len(c.terraform) > 0 {
		out["terraform"] = c.terraform
	}
	if len(c.providers) > 0 {
		out["provider"] = c.providers
	}
	if len(c.resources) > 0 {
		out["resource"] = c.resources
	}
	if len(c.dataSrcs) > 0 {
		out["data"] = c.dataSrcs
	}
	// encoding/json sorts map keys alphabetically → stable, diff-friendly output.
	return json.MarshalIndent(out, "", "  ")
}
