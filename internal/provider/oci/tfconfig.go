package oci

// tfConfig is a builder for `.tf.json` documents using the canonical JSON-config
// object form (the array-of-blocks form is valid but unnecessary here; a single
// block per type keeps diffs clean).
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
type tfConfig struct {
	terraform obj // single terraform block body (required_providers, backend, …)
	providers obj // provider local-name → config
	resources obj // TYPE → (NAME → body)
	dataSrcs  obj // TYPE → (NAME → body)
}

// obj is a JSON object. encoding/json sorts map keys alphabetically on marshal,
// which gives stable, diff-friendly output (no manual ordering needed).
type obj = map[string]interface{}

type reqProvider struct {
	name    string
	source  string
	version string // optional; "" ⇒ omit
}

func newTFConfig() tfConfig {
	return tfConfig{
		terraform: obj{},
		providers: obj{},
		resources: obj{},
		dataSrcs:  obj{},
	}
}

// addRequiredProviders merges all given providers into a single
// `required_providers` object under the terraform block.
func (c *tfConfig) addRequiredProviders(ps ...reqProvider) {
	rps, ok := c.terraform["required_providers"].(obj)
	if !ok {
		rps = obj{}
		c.terraform["required_providers"] = rps
	}
	for _, p := range ps {
		entry := obj{"source": p.source}
		if p.version != "" {
			entry["version"] = p.version
		}
		rps[p.name] = entry
	}
}

func (c *tfConfig) addProvider(name string, body obj) {
	c.providers[name] = body
}

// addResource merges a resource of the given TYPE/NAME into the config.
func (c *tfConfig) addResource(typ, name string, body obj) {
	byName, ok := c.resources[typ].(obj)
	if !ok {
		byName = obj{}
		c.resources[typ] = byName
	}
	byName[name] = body
}

func (c *tfConfig) addDataSource(typ, name string, body obj) {
	byName, ok := c.dataSrcs[typ].(obj)
	if !ok {
		byName = obj{}
		c.dataSrcs[typ] = byName
	}
	byName[name] = body
}

// MarshalJSON renders the config, omitting empty top-level sections.
func (c tfConfig) MarshalJSON() ([]byte, error) {
	out := obj{}
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
	return jsonMarshal(out)
}

var _ = jsonMarshal // see jsonMarshal in json.go
