package state

import (
	"database/sql"
	"encoding/json"
	"time"
)

// StoreAPI is the broad application-facing store surface.
//
// It exists to decouple production packages from the concrete SQLite-backed
// *Store implementation. Main and tests may still work with *Store directly
// (Open, Close, SetBus, etc.), while application code depends on this
// interface so a future backend swap or fake store for tests does not require
// importing the concrete type everywhere.
//
// This is intentionally broad as a first extraction step; narrower read/write
// seams can be carved out later once the concrete coupling is removed.
type StoreAPI interface {
	DB() *sql.DB

	CreateResource(resourceType, name string, spec, status any, labels, annotations map[string]string) (Metadata, error)
	GetResource(resourceType, name string, spec, status any) (Metadata, error)
	ListResources(resourceType string, opts ListOptions) ([]Metadata, []json.RawMessage, []json.RawMessage, int, error)
	UpdateResource(resourceType, name string, currentVersion int64, spec any, labels, annotations map[string]string) (Metadata, error)
	UpdateStatus(resourceType, name string, status any) (Metadata, error)
	DeleteResource(resourceType, name string) (Metadata, error)
	RemoveResource(resourceType, name string) error
	RemoveResourcesByTenant(tenant string) (int, error)
	AddFinalizer(resourceType, name, finalizer string) error
	RemoveFinalizer(resourceType, name, finalizer string) (bool, error)

	CreateTenant(name string, spec TenantSpec, labels, annotations map[string]string) (*Tenant, error)
	GetTenant(name string) (*Tenant, error)
	ListTenants(opts ...ListOption) ([]*Tenant, int, error)
	UpdateTenantSpec(name string, currentVersion int64, spec TenantSpec, labels, annotations map[string]string) (*Tenant, error)
	UpdateTenantStatus(name string, status TenantStatus) (*Tenant, error)
	DeleteTenant(name string) error
	SaveTenantSecrets(name string, bundle []byte) error
	LoadTenantSecrets(name string) ([]byte, error)
	RemoveTenantSecrets(name string) error

	CreateMachine(id string, spec MachineSpec, labels, annotations map[string]string) (*Machine, error)
	GetMachine(id string) (*Machine, error)
	ListMachines(opts ...ListOption) ([]*Machine, int, error)
	ListMachinesByTenant(tenantName string, opts ...ListOption) ([]*Machine, int, error)
	UpdateMachineStatus(id string, status MachineStatus) (*Machine, error)
	DeleteMachine(id string) error

	UpsertProvider(providerType string, spec ProviderSpec, status ProviderStatus, labels map[string]string) (*Provider, error)
	GetProvider(providerType string) (*Provider, error)
	ListProviders() ([]*Provider, error)

	CreateUser(name string, spec UserSpec) (*User, error)
	GetUser(name string) (*User, error)
	ListUsers() ([]*User, error)
	UpdateUser(name string, currentVersion int64, spec UserSpec) (*User, error)
	DeleteUser(name string) error
	UpdateUserLastLogin(name string) error

	CreateAPIToken(id, userName, tokenHash string, expiresAt *time.Time) (*APIToken, error)
	GetAPIToken(id string) (*APIToken, error)
	ListAPITokens(userName string) ([]*APIToken, error)
	LookupAPITokenByHash(tokenHash string) (*APIToken, error)
	TouchAPIToken(id string) error
	DeleteAPIToken(id string) error
}
