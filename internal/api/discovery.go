package api

// APIResource describes a single resource type exposed by the REST API. It is
// the K8s APIResource shape (k8s.io/apimachinery/pkg/apis/meta/v1.APIResource)
// so standard tooling can discover what rezuscloud serves.
type APIResource struct {
	Name         string   `json:"name"`          // plural, e.g. "tenants"
	SingularName string   `json:"singularName"`  // e.g. "tenant"
	Kind         string   `json:"kind"`          // e.g. "Tenant"
	Namespaced   bool     `json:"namespaced"`    // tenant-scoped? (rezuscloud uses tenant labels, not k8s namespaces)
	Verbs        []string `json:"verbs"`         // supported operations
}

// APIResourceList is the top-level discovery response (K8s shape).
type APIResourceList struct {
	APIVersion string         `json:"apiVersion"` // "v1"
	Kind       string         `json:"kind"`       // "APIResourceList"
	Resources  []APIResource  `json:"resources"`
}

// DefaultAPIResourceList returns the static catalogue of resource types the REST API
// serves. This is the response for GET /api/v1 — the discovery endpoint (#174).
// It mirrors the routes registered in Router().
func DefaultAPIResourceList() APIResourceList {
	return APIResourceList{
		APIVersion: "v1",
		Kind:       "APIResourceList",
		Resources: []APIResource{
			{Name: "tenants", SingularName: "tenant", Kind: "Tenant", Namespaced: false,
				Verbs: []string{"get", "list", "create", "update", "delete", "watch"}},
			{Name: "node-groups", SingularName: "node-group", Kind: "NodeGroup", Namespaced: true,
				Verbs: []string{"get", "list", "create", "update", "delete", "watch"}},
			{Name: "machines", SingularName: "machine", Kind: "Machine", Namespaced: false,
				Verbs: []string{"get", "list", "delete", "watch"}},
			{Name: "configpatches", SingularName: "configpatch", Kind: "ConfigPatch", Namespaced: true,
				Verbs: []string{"get", "list", "create", "update", "delete", "watch"}},
			{Name: "providers", SingularName: "provider", Kind: "Provider", Namespaced: false,
				Verbs: []string{"get", "list"}},
			{Name: "users", SingularName: "user", Kind: "User", Namespaced: false,
				Verbs: []string{"get", "list", "create", "update", "delete"}},
			{Name: "api-tokens", SingularName: "api-token", Kind: "APIToken", Namespaced: false,
				Verbs: []string{"get", "list", "create", "delete"}},
		},
	}
}
