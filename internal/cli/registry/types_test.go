package registry

import "testing"

func TestRegistry_Resolve(t *testing.T) {
	reg := New()

	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"cluster", "Cluster", false},
		{"clusters", "Cluster", false},
		{"Cluster", "Cluster", false},
		{"machine", "Machine", false},
		{"machines", "Machine", false},
		{"ng", "NodeGroup", false},
		{"nodegroup", "NodeGroup", false},
		{"provider", "Provider", false},
		{"jt", "JoinToken", false},
		{"jointoken", "JoinToken", false},
		{"patch", "ConfigPatch", false},
		{"user", "User", false},
		{"unknown", "", true},
		{"pod", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := reg.Resolve(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tt.input, err)
			}
			if got.Kind != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.input, got.Kind, tt.want)
			}
		})
	}
}

func TestResourceType_APIPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		scope   Scope
		cluster string
		want    string
		wantErr bool
	}{
		{
			name:  "cluster-wide no cluster needed",
			path:  "api/v1/tenants",
			scope: ScopeCluster,
			want:  "api/v1/tenants",
		},
		{
			name:    "scoped with cluster",
			path:    "api/v1/tenants/{cluster}/node-groups",
			scope:   ScopeClusterRequired,
			cluster: "prod",
			want:    "api/v1/tenants/prod/node-groups",
		},
		{
			name:    "scoped without cluster",
			path:    "api/v1/tenants/{cluster}/node-groups",
			scope:   ScopeClusterRequired,
			cluster: "",
			wantErr: true,
		},
		{
			name:  "optional scope without cluster",
			path:  "api/v1/machines",
			scope: ScopeClusterOptional,
			want:  "api/v1/machines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := ResourceType{Path: tt.path, Scope: tt.scope, Names: []string{"test"}}
			got, err := rt.APIPath(tt.cluster)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("APIPath: %v", err)
			}
			if got != tt.want {
				t.Errorf("APIPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResourceType_SupportsVerb(t *testing.T) {
	reg := New()

	machine, _ := reg.Resolve("machine")
	if machine.SupportsVerb("get") != true {
		t.Error("machine should support get")
	}
	if machine.SupportsVerb("create") != false {
		t.Error("machine should not support create")
	}

	cluster, _ := reg.Resolve("cluster")
	if cluster.SupportsVerb("delete") != true {
		t.Error("cluster should support delete")
	}
}

func TestRegistry_All(t *testing.T) {
	reg := New()
	all := reg.All()
	if len(all) != 7 {
		t.Errorf("expected 7 resource types, got %d", len(all))
	}
}
