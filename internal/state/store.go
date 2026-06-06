// Package state provides the persistent state store for the management plane.
// SQLite backend — lightweight, reliable, works on any filesystem.
// All resources follow the K8s three-section pattern: metadata, spec, status.
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// --- Metadata ---

// Metadata contains identity and bookkeeping for every resource.
type Metadata struct {
	Name              string            `json:"name"`
	UID               string            `json:"uid"`
	ResourceVersion   int64             `json:"resourceVersion"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
	Finalizers        []string          `json:"finalizers,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

// --- Tenant ---

// Tenant represents a tenant cluster.
type Tenant struct {
	Metadata Metadata     `json:"metadata"`
	Spec     TenantSpec   `json:"spec"`
	Status   TenantStatus `json:"status"`
}

// TenantSpec is the user-declared desired state of a tenant.
type TenantSpec struct {
	KubernetesVersion    string           `json:"kubernetesVersion"`
	TalosVersion         string           `json:"talosVersion,omitempty"`
	ControlPlaneEndpoint string           `json:"controlPlaneEndpoint,omitempty"`
	PodNetwork           []string         `json:"podNetwork,omitempty"`
	ServiceNetwork       []string         `json:"serviceNetwork,omitempty"`
	Plugins              *TenantPlugins   `json:"plugins,omitempty"`
	NodeGroups           []NodeGroupSpec  `json:"nodeGroups,omitempty"`
	ConfigPatches        []ConfigPatchRef `json:"configPatches,omitempty"`
}

// TenantPlugins defines cluster-level add-ons (CNI, CSI, etc.).
type TenantPlugins struct {
	CNI *PluginSpec `json:"cni,omitempty"`
	CSI *PluginSpec `json:"csi,omitempty"`
}

// PluginSpec describes a cluster plugin.
type PluginSpec struct {
	Type    string `json:"type"`
	Version string `json:"version,omitempty"`
	Values  string `json:"values,omitempty"`
}

// NodeGroupSpec defines a group of machines within a tenant.
type NodeGroupSpec struct {
	Name           string           `json:"name"`
	Role           string           `json:"role"` // "controlplane" or "worker"
	Count          int              `json:"count"`
	ProviderClass  string           `json:"providerClass,omitempty"`
	ProviderConfig json.RawMessage  `json:"providerConfig,omitempty"`
	TalosVersion   string           `json:"talosVersion,omitempty"`
	ConfigPatches  []ConfigPatchRef `json:"configPatches,omitempty"`
}

// ConfigPatchRef references a config patch by name.
type ConfigPatchRef struct {
	Name string `json:"name"`
}

// TenantPhase represents the lifecycle phase of a tenant.
type TenantPhase string

const (
	TenantForming   TenantPhase = "forming"
	TenantShrinking TenantPhase = "shrinking"
	TenantActive    TenantPhase = "active"
	TenantRemoving  TenantPhase = "removing"
)

// TenantStatus is the system-observed actual state of a tenant.
type TenantStatus struct {
	Phase             TenantPhase   `json:"phase"`
	Available         bool          `json:"available"`
	Ready             bool          `json:"ready"`
	APIReady          bool          `json:"apiReady"`
	ControlPlaneReady bool          `json:"controlPlaneReady"`
	Machines          MachineCounts `json:"machines"`
	KubernetesVersion string        `json:"kubernetesVersion,omitempty"`
	TalosVersion      string        `json:"talosVersion,omitempty"`
}

// MachineCounts tracks machine health counts.
type MachineCounts struct {
	Total     int `json:"total"`
	Healthy   int `json:"healthy"`
	Connected int `json:"connected"`
}

// --- Machine ---

// Machine represents a physical or virtual machine.
type Machine struct {
	Metadata Metadata      `json:"metadata"`
	Spec     MachineSpec   `json:"spec"`
	Status   MachineStatus `json:"status"`
}

// MachineSpec is the management plane's view of a machine.
type MachineSpec struct {
	ManagementAddress string `json:"managementAddress,omitempty"`
	Connected         bool   `json:"connected"`
}

// MachineStage represents the lifecycle stage of a machine.
type MachineStage string

const (
	StageInitializing MachineStage = "initializing"
	StageInstalling   MachineStage = "installing"
	StageConfiguring  MachineStage = "configuring"
	StageReady        MachineStage = "ready"
	StageRestarting   MachineStage = "restarting"
	StageStopping     MachineStage = "stopping"
	StageOff          MachineStage = "off"
	StageUpdating     MachineStage = "updating"
	StageRemoving     MachineStage = "removing"
)

// MachineStatus is the system-observed state of a machine.
type MachineStatus struct {
	Stage         MachineStage   `json:"stage"`
	Ready         bool           `json:"ready"`
	Role          string         `json:"role,omitempty"` // "controlplane" or "worker"
	TalosVersion  string         `json:"talosVersion,omitempty"`
	K8sVersion    string         `json:"kubernetesVersion,omitempty"`
	ConfigCurrent bool           `json:"configUpToDate"`
	Maintenance   bool           `json:"maintenance"`
	Hardware      *HardwareInfo  `json:"hardware,omitempty"`
	Network       *NetworkInfo   `json:"network,omitempty"`
	Schematic     *SchematicInfo `json:"schematic,omitempty"`
	LastError     string         `json:"lastError,omitempty"`
}

// HardwareInfo describes machine hardware.
type HardwareInfo struct {
	Processors    []ProcessorInfo   `json:"processors,omitempty"`
	MemoryModules []MemoryInfo      `json:"memoryModules,omitempty"`
	BlockDevices  []BlockDeviceInfo `json:"blockDevices,omitempty"`
	Arch          string            `json:"arch,omitempty"`
}

// ProcessorInfo describes a CPU.
type ProcessorInfo struct {
	CoreCount   int    `json:"coreCount"`
	ThreadCount int    `json:"threadCount"`
	Frequency   int    `json:"frequency,omitempty"`
	Description string `json:"description,omitempty"`
}

// MemoryInfo describes a memory module.
type MemoryInfo struct {
	SizeMB      int    `json:"sizeMb"`
	Description string `json:"description,omitempty"`
}

// BlockDeviceInfo describes a disk.
type BlockDeviceInfo struct {
	Size       int64  `json:"size"`
	Type       string `json:"type,omitempty"`
	SystemDisk bool   `json:"systemDisk"`
	Model      string `json:"model,omitempty"`
}

// NetworkInfo describes machine networking.
type NetworkInfo struct {
	Hostname        string   `json:"hostname,omitempty"`
	Addresses       []string `json:"addresses,omitempty"`
	DefaultGateways []string `json:"defaultGateways,omitempty"`
}

// SchematicInfo describes the Talos image schematic.
type SchematicInfo struct {
	ID         string   `json:"id,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

// --- Provider ---

// Provider represents a connected infrastructure provider.
type Provider struct {
	Metadata Metadata       `json:"metadata"`
	Spec     ProviderSpec   `json:"spec"`
	Status   ProviderStatus `json:"status"`
}

// ProviderSpec is the user-declared state of a provider.
type ProviderSpec struct {
	Endpoint string `json:"endpoint,omitempty"`
}

// ProviderStatus is the system-observed state of a provider.
type ProviderStatus struct {
	Connected     bool            `json:"connected"`
	LastHeartbeat time.Time       `json:"lastHeartbeat,omitempty"`
	Schema        *ProviderSchema `json:"schema,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// ProviderSchema describes provider capabilities.
type ProviderSchema struct {
	MachineTypes []string `json:"machineTypes,omitempty"`
	Regions      []string `json:"regions,omitempty"`
}

// --- JoinToken ---

// JoinToken maps a booting machine to a tenant node group.
type JoinToken struct {
	Metadata Metadata        `json:"metadata"`
	Spec     JoinTokenSpec   `json:"spec"`
	Status   JoinTokenStatus `json:"status"`
}

// JoinTokenSpec is the configuration of a join token.
type JoinTokenSpec struct {
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	SingleUse bool      `json:"singleUse"`
	NodeGroup string    `json:"nodeGroup,omitempty"`
}

// JoinTokenStatus tracks token usage.
type JoinTokenStatus struct {
	Used   bool       `json:"used"`
	UsedBy string     `json:"usedBy,omitempty"`
	UsedAt *time.Time `json:"usedAt,omitempty"`
}

// --- User ---

// User represents an authenticated identity.
type User struct {
	Metadata Metadata   `json:"metadata"`
	Spec     UserSpec   `json:"spec"`
	Status   UserStatus `json:"status"`
}

// UserSpec is the user's configuration.
type UserSpec struct {
	Role         string `json:"role"` // "view", "edit", "admin"
	PasswordHash string `json:"passwordHash"`
}

// UserStatus tracks user session info.
type UserStatus struct {
	LastLogin    *time.Time `json:"lastLogin,omitempty"`
	ActiveTokens int        `json:"activeTokens"`
}

// --- Store ---

// ResourceEvent is reported by the store to an external observer (e.g. watch bus).
// This decouples the store from the watch package (no import cycle) and lets the
// store run without a bus in tests.
type ResourceEvent struct {
	Type         string // "ADDED", "MODIFIED", "DELETED"
	ResourceType string // "tenant", "machine", ...
	Metadata     Metadata
	Spec         json.RawMessage
	Status       json.RawMessage
}

// EventBus is the observer interface the store notifies on mutations.
// Implementations must be safe for concurrent use.
type EventBus interface {
	Publish(resourceType string, event ResourceEvent)
}

// Store is the persistent state store for the management plane.
type Store struct {
	db  *sql.DB
	bus EventBus // optional — set via SetBus
}

// SetBus attaches an event bus. Safe to call once at startup before any mutation.
func (s *Store) SetBus(bus EventBus) {
	s.bus = bus
}

// publish notifies the bus if attached. Best-effort — errors are swallowed.
func (s *Store) publish(eventType, resourceType string, md Metadata, spec, status json.RawMessage) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(resourceType, ResourceEvent{
		Type:         eventType,
		ResourceType: resourceType,
		Metadata:     md,
		Spec:         spec,
		Status:       status,
	})
}

// Open creates or opens a SQLite database at the given path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	// modernc.org/sqlite driver name is "sqlite".
	// DSN parameters use the standard SQLite pragma syntax (_journal_mode, _busy_timeout).
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database (for advanced queries).
func (s *Store) DB() *sql.DB {
	return s.db
}

// --- Migration ---

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS resources (
		type TEXT NOT NULL,
		name TEXT NOT NULL,
		uid TEXT NOT NULL,
		version INTEGER NOT NULL DEFAULT 1,
		spec TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT '{}',
		finalizers TEXT NOT NULL DEFAULT '[]',
		labels TEXT NOT NULL DEFAULT '{}',
		annotations TEXT NOT NULL DEFAULT '{}',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		deletion_timestamp DATETIME DEFAULT NULL,
		rowid INTEGER PRIMARY KEY AUTOINCREMENT,
		UNIQUE(type, name)
	);

	CREATE INDEX IF NOT EXISTS idx_resources_type ON resources(type);
	CREATE INDEX IF NOT EXISTS idx_resources_labels ON resources(labels);

	CREATE TABLE IF NOT EXISTS tenant_secrets (
		tenant_name TEXT NOT NULL PRIMARY KEY,
		bundle BLOB NOT NULL,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (tenant_name) REFERENCES resources(name) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS api_tokens (
		id TEXT NOT NULL PRIMARY KEY,
		user_name TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		expires_at DATETIME,
		created_at DATETIME NOT NULL,
		last_used DATETIME,
		FOREIGN KEY (user_name) REFERENCES resources(name) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_name);

	CREATE TABLE IF NOT EXISTS audit_events (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp   TEXT NOT NULL,
		user_name   TEXT,
		role        TEXT,
		method      TEXT NOT NULL,
		path        TEXT NOT NULL,
		resource    TEXT,
		resource_id TEXT,
		verb        TEXT,
		status      INTEGER NOT NULL,
		request_id  TEXT,
		source_ip   TEXT,
		error       TEXT,
		metadata    TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_events(user_name);
	CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_events(resource);
	`

	_, err := s.db.Exec(schema)
	return err
}

// --- Generic Resource Operations ---

// CreateResource inserts a new resource.
func (s *Store) CreateResource(resourceType, name string, spec, status any, labels, annotations map[string]string) (metadata Metadata, err error) {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal spec: %w", err)
	}

	statusJSON, err := json.Marshal(status)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal status: %w", err)
	}

	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal labels: %w", err)
	}

	annotationsJSON, err := json.Marshal(annotations)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal annotations: %w", err)
	}

	now := time.Now().UTC()
	uid := newUID()

	result, err := s.db.Exec(
		`INSERT INTO resources (type, name, uid, spec, status, finalizers, labels, annotations, created_at, updated_at, version)
		 VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?, ?, 1)`,
		resourceType, name, uid, string(specJSON), string(statusJSON), string(labelsJSON), string(annotationsJSON), now, now,
	)
	if err != nil {
		return Metadata{}, fmt.Errorf("insert resource: %w", err)
	}

	version, _ := result.LastInsertId()

	md := Metadata{
		Name:            name,
		UID:             uid,
		ResourceVersion: version,
		CreatedAt:       now,
		UpdatedAt:       now,
		Labels:          labels,
		Annotations:     annotations,
	}

	s.publish("ADDED", resourceType, md, specJSON, statusJSON)

	return md, nil
}

// GetResource reads a single resource.
func (s *Store) GetResource(resourceType, name string, spec, status any) (Metadata, error) {
	row := s.db.QueryRow(
		`SELECT uid, version, finalizers, labels, annotations, created_at, updated_at, deletion_timestamp, spec, status
		 FROM resources WHERE type = ? AND name = ?`,
		resourceType, name,
	)

	var md Metadata
	var finalizersJSON, labelsJSON, annotationsJSON, specJSON, statusJSON string
	var deletionTimestamp sql.NullTime

	err := row.Scan(
		&md.UID, &md.ResourceVersion, &finalizersJSON, &labelsJSON, &annotationsJSON,
		&md.CreatedAt, &md.UpdatedAt, &deletionTimestamp,
		&specJSON, &statusJSON,
	)
	if err == sql.ErrNoRows {
		return Metadata{}, ErrNotFound
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("scan resource: %w", err)
	}

	md.Name = name

	if deletionTimestamp.Valid {
		md.DeletionTimestamp = &deletionTimestamp.Time
	}

	_ = json.Unmarshal([]byte(finalizersJSON), &md.Finalizers)
	_ = json.Unmarshal([]byte(labelsJSON), &md.Labels)
	_ = json.Unmarshal([]byte(annotationsJSON), &md.Annotations)

	if spec != nil {
		_ = json.Unmarshal([]byte(specJSON), spec)
	}
	if status != nil {
		_ = json.Unmarshal([]byte(statusJSON), status)
	}

	return md, nil
}

// ListResources reads multiple resources of a given type.
func (s *Store) ListResources(resourceType string, opts ListOptions) ([]Metadata, []json.RawMessage, []json.RawMessage, int, error) {
	// Count total with same filters.
	countQuery := `SELECT COUNT(*) FROM resources WHERE type = ? AND deletion_timestamp IS NULL`
	countArgs := []any{resourceType}

	if opts.LabelSelector != "" {
		if idx := byteIndex(opts.LabelSelector, '='); idx >= 0 {
			key := opts.LabelSelector[:idx]
			val := opts.LabelSelector[idx+1:]
			countQuery += fmt.Sprintf(` AND json_extract(labels, '$.%s') = ?`, jsonPathKey(key))
			countArgs = append(countArgs, val)
		} else {
			countQuery += fmt.Sprintf(` AND json_extract(labels, '$.%s') IS NOT NULL`, jsonPathKey(opts.LabelSelector))
		}
	}

	var total int
	countErr := s.db.QueryRow(countQuery, countArgs...).Scan(&total)
	if countErr != nil {
		return nil, nil, nil, 0, fmt.Errorf("count resources: %w", countErr)
	}

	query := `SELECT name, uid, version, finalizers, labels, annotations, created_at, updated_at, deletion_timestamp, spec, status
			  FROM resources WHERE type = ? AND deletion_timestamp IS NULL`

	args := []any{resourceType}

	if opts.LabelSelector != "" {
		// Parse simple key=value selector.
		if idx := byteIndex(opts.LabelSelector, '='); idx >= 0 {
			key := opts.LabelSelector[:idx]
			val := opts.LabelSelector[idx+1:]
			query += fmt.Sprintf(` AND json_extract(labels, '$.%s') = ?`, jsonPathKey(key))
			args = append(args, val)
		} else {
			// Key existence selector.
			query += fmt.Sprintf(` AND json_extract(labels, '$.%s') IS NOT NULL`, jsonPathKey(opts.LabelSelector))
		}
	}

	query += ` ORDER BY name`

	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, opts.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("query resources: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var metadata []Metadata
	var specs []json.RawMessage
	var statuses []json.RawMessage

	for rows.Next() {
		var md Metadata
		var finalizersJSON, labelsJSON, annotationsJSON string
		var specJSON, statusJSON string
		var deletionTimestamp sql.NullTime

		err := rows.Scan(
			&md.Name, &md.UID, &md.ResourceVersion, &finalizersJSON, &labelsJSON, &annotationsJSON,
			&md.CreatedAt, &md.UpdatedAt, &deletionTimestamp,
			&specJSON, &statusJSON,
		)
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("scan resource: %w", err)
		}

		if deletionTimestamp.Valid {
			md.DeletionTimestamp = &deletionTimestamp.Time
		}

		_ = json.Unmarshal([]byte(finalizersJSON), &md.Finalizers)
		_ = json.Unmarshal([]byte(labelsJSON), &md.Labels)
		_ = json.Unmarshal([]byte(annotationsJSON), &md.Annotations)

		metadata = append(metadata, md)
		specs = append(specs, json.RawMessage(specJSON))
		statuses = append(statuses, json.RawMessage(statusJSON))
	}

	return metadata, specs, statuses, total, rows.Err()
}

// updateResourceMetadataAndPublish reloads a row after a mutation and publishes a bus event.
// Centralizes the reload+publish pattern used by UpdateResource/UpdateStatus/DeleteResource.
func (s *Store) updateResourceMetadataAndPublish(eventType, resourceType, name string) (Metadata, error) {
	var (
		md                                          Metadata
		finalizersJSON, labelsJSON, annotationsJSON string
		specJSON, statusJSON                        string
		deletionTimestamp                           sql.NullTime
	)
	err := s.db.QueryRow(
		`SELECT name, uid, version, finalizers, labels, annotations, created_at, updated_at, deletion_timestamp, spec, status
		 FROM resources WHERE type = ? AND name = ?`,
		resourceType, name,
	).Scan(
		&md.Name, &md.UID, &md.ResourceVersion, &finalizersJSON, &labelsJSON, &annotationsJSON,
		&md.CreatedAt, &md.UpdatedAt, &deletionTimestamp, &specJSON, &statusJSON,
	)
	if err != nil {
		return Metadata{}, fmt.Errorf("reload resource: %w", err)
	}

	_ = json.Unmarshal([]byte(finalizersJSON), &md.Finalizers)
	_ = json.Unmarshal([]byte(labelsJSON), &md.Labels)
	_ = json.Unmarshal([]byte(annotationsJSON), &md.Annotations)
	if deletionTimestamp.Valid {
		t := deletionTimestamp.Time
		md.DeletionTimestamp = &t
	}

	s.publish(eventType, resourceType, md, json.RawMessage(specJSON), json.RawMessage(statusJSON))

	return md, nil
}

// UpdateResource updates spec of an existing resource. Checks resourceVersion for optimistic concurrency.
func (s *Store) UpdateResource(resourceType, name string, currentVersion int64, spec any, labels, annotations map[string]string) (Metadata, error) {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal spec: %w", err)
	}

	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal labels: %w", err)
	}

	annotationsJSON, err := json.Marshal(annotations)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal annotations: %w", err)
	}

	now := time.Now().UTC()

	result, err := s.db.Exec(
		`UPDATE resources SET spec = ?, labels = ?, annotations = ?, updated_at = ?, version = version + 1
		 WHERE type = ? AND name = ? AND version = ?`,
		string(specJSON), string(labelsJSON), string(annotationsJSON), now,
		resourceType, name, currentVersion,
	)
	if err != nil {
		return Metadata{}, fmt.Errorf("update resource: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Metadata{}, ErrConflict
	}

	return s.updateResourceMetadataAndPublish("MODIFIED", resourceType, name)
}

// UpdateStatus updates only the status section of a resource.
func (s *Store) UpdateStatus(resourceType, name string, status any) (Metadata, error) {
	statusJSON, err := json.Marshal(status)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal status: %w", err)
	}

	now := time.Now().UTC()

	result, err := s.db.Exec(
		`UPDATE resources SET status = ?, updated_at = ?
		 WHERE type = ? AND name = ?`,
		string(statusJSON), now, resourceType, name,
	)
	if err != nil {
		return Metadata{}, fmt.Errorf("update status: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Metadata{}, ErrNotFound
	}

	return s.updateResourceMetadataAndPublish("MODIFIED", resourceType, name)
}

// DeleteResource sets deletionTimestamp on a resource (finalizer-controlled deletion).
func (s *Store) DeleteResource(resourceType, name string) (Metadata, error) {
	now := time.Now().UTC()

	result, err := s.db.Exec(
		`UPDATE resources SET deletion_timestamp = ?, updated_at = ?
		 WHERE type = ? AND name = ? AND deletion_timestamp IS NULL`,
		now, now, resourceType, name,
	)
	if err != nil {
		return Metadata{}, fmt.Errorf("delete resource: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Metadata{}, ErrNotFound
	}

	return s.updateResourceMetadataAndPublish("DELETED", resourceType, name)
}

// RemoveResource permanently deletes a resource from the database.
// Called after all finalizers are cleared.
func (s *Store) RemoveResource(resourceType, name string) error {
	// Snapshot before delete so we can publish the final removal event.
	md, _ := s.GetResource(resourceType, name, nil, nil)

	_, err := s.db.Exec(
		`DELETE FROM resources WHERE type = ? AND name = ?`,
		resourceType, name,
	)
	if err != nil {
		return fmt.Errorf("remove resource: %w", err)
	}

	if md.Name != "" {
		s.publish("DELETED", resourceType, md, nil, nil)
	}
	return nil
}

// --- Finalizer Operations ---

// AddFinalizer adds a finalizer to a resource.
func (s *Store) AddFinalizer(resourceType, name, finalizer string) error {
	md, err := s.GetResource(resourceType, name, nil, nil)
	if err != nil {
		return err
	}

	for _, f := range md.Finalizers {
		if f == finalizer {
			return nil // already present
		}
	}

	md.Finalizers = append(md.Finalizers, finalizer)
	finalizersJSON, _ := json.Marshal(md.Finalizers)

	_, err = s.db.Exec(
		`UPDATE resources SET finalizers = ?, updated_at = ? WHERE type = ? AND name = ?`,
		string(finalizersJSON), time.Now().UTC(), resourceType, name,
	)
	return err
}

// RemoveFinalizer removes a finalizer from a resource.
// If no finalizers remain and deletionTimestamp is set, the resource is permanently deleted.
func (s *Store) RemoveFinalizer(resourceType, name, finalizer string) (removed bool, err error) {
	md, err := s.GetResource(resourceType, name, nil, nil)
	if err != nil {
		return false, err
	}

	newFinalizers := make([]string, 0, len(md.Finalizers))
	for _, f := range md.Finalizers {
		if f != finalizer {
			newFinalizers = append(newFinalizers, f)
		}
	}

	if len(newFinalizers) == len(md.Finalizers) {
		return false, nil // finalizer not found
	}

	if len(newFinalizers) == 0 && md.DeletionTimestamp != nil {
		// All finalizers cleared and resource is pending deletion — remove permanently.
		_ = s.RemoveResource(resourceType, name)
		return true, nil
	}

	finalizersJSON, _ := json.Marshal(newFinalizers)
	_, err = s.db.Exec(
		`UPDATE resources SET finalizers = ?, updated_at = ? WHERE type = ? AND name = ?`,
		string(finalizersJSON), time.Now().UTC(), resourceType, name,
	)
	return true, err
}

// --- Tenant-Specific Operations ---

// CreateTenant creates a tenant resource.
func (s *Store) CreateTenant(name string, spec TenantSpec, labels, annotations map[string]string) (*Tenant, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}

	status := TenantStatus{Phase: TenantForming}

	md, err := s.CreateResource("tenant", name, spec, status, labels, annotations)
	if err != nil {
		return nil, err
	}

	return &Tenant{Metadata: md, Spec: spec, Status: status}, nil
}

// GetTenant returns a tenant by name.
func (s *Store) GetTenant(name string) (*Tenant, error) {
	var spec TenantSpec
	var status TenantStatus

	md, err := s.GetResource("tenant", name, &spec, &status)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &Tenant{Metadata: md, Spec: spec, Status: status}, nil
}

// ListTenants returns all tenants.
func (s *Store) ListTenants(opts ...ListOption) ([]*Tenant, int, error) {
	o := newListOptions(opts...)
	mds, specs, statuses, total, err := s.ListResources("tenant", o)
	if err != nil {
		return nil, 0, err
	}

	tenants := make([]*Tenant, 0, len(mds))
	for i := range mds {
		var spec TenantSpec
		var status TenantStatus
		_ = json.Unmarshal(specs[i], &spec)
		_ = json.Unmarshal(statuses[i], &status)
		tenants = append(tenants, &Tenant{Metadata: mds[i], Spec: spec, Status: status})
	}

	return tenants, total, nil
}

// UpdateTenantSpec updates a tenant's spec.
func (s *Store) UpdateTenantSpec(name string, currentVersion int64, spec TenantSpec, labels, annotations map[string]string) (*Tenant, error) {
	md, err := s.UpdateResource("tenant", name, currentVersion, spec, labels, annotations)
	if err != nil {
		return nil, err
	}

	var status TenantStatus
	_, _ = s.GetResource("tenant", name, nil, &status)

	return &Tenant{Metadata: md, Spec: spec, Status: status}, nil
}

// UpdateTenantStatus updates a tenant's status (controllers only).
func (s *Store) UpdateTenantStatus(name string, status TenantStatus) (*Tenant, error) {
	md, err := s.UpdateStatus("tenant", name, status)
	if err != nil {
		return nil, err
	}

	var spec TenantSpec
	_, _ = s.GetResource("tenant", name, &spec, nil)

	return &Tenant{Metadata: md, Spec: spec, Status: status}, nil
}

// DeleteTenant sets deletionTimestamp on a tenant.
func (s *Store) DeleteTenant(name string) error {
	_, err := s.DeleteResource("tenant", name)
	if err != nil {
		return err
	}

	// Add finalizers for controlled teardown.
	_ = s.AddFinalizer("tenant", name, "rezuscloud.io/machines")
	_ = s.AddFinalizer("tenant", name, "rezuscloud.io/secrets")
	_ = s.AddFinalizer("tenant", name, "rezuscloud.io/tokens")

	return nil
}

// SaveTenantSecrets stores the secrets bundle for a tenant.
// Overwrites any existing bundle for the same tenant.
func (s *Store) SaveTenantSecrets(name string, bundle []byte) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO tenant_secrets (tenant_name, bundle, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(tenant_name) DO UPDATE SET bundle = excluded.bundle, created_at = excluded.created_at
	`, name, bundle, now)
	if err != nil {
		return fmt.Errorf("save tenant secrets: %w", err)
	}
	return nil
}

// LoadTenantSecrets returns the secrets bundle bytes for a tenant, or nil if none.
func (s *Store) LoadTenantSecrets(name string) ([]byte, error) {
	var bundle []byte
	err := s.db.QueryRow(`SELECT bundle FROM tenant_secrets WHERE tenant_name = ?`, name).Scan(&bundle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load tenant secrets: %w", err)
	}
	return bundle, nil
}

// RemoveTenantSecrets deletes the secrets bundle for a tenant.
// No-op if no bundle exists.
func (s *Store) RemoveTenantSecrets(name string) error {
	_, err := s.db.Exec(`DELETE FROM tenant_secrets WHERE tenant_name = ?`, name)
	if err != nil {
		return fmt.Errorf("remove tenant secrets: %w", err)
	}
	return nil
}

// --- Machine-Specific Operations ---

// CreateMachine creates a machine resource.
func (s *Store) CreateMachine(id string, spec MachineSpec, labels, annotations map[string]string) (*Machine, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}

	status := MachineStatus{Stage: StageInitializing}

	md, err := s.CreateResource("machine", id, spec, status, labels, annotations)
	if err != nil {
		return nil, err
	}

	return &Machine{Metadata: md, Spec: spec, Status: status}, nil
}

// GetMachine returns a machine by ID.
func (s *Store) GetMachine(id string) (*Machine, error) {
	var spec MachineSpec
	var status MachineStatus

	md, err := s.GetResource("machine", id, &spec, &status)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &Machine{Metadata: md, Spec: spec, Status: status}, nil
}

// ListMachines returns all machines.
func (s *Store) ListMachines(opts ...ListOption) ([]*Machine, int, error) {
	o := newListOptions(opts...)
	mds, specs, statuses, total, err := s.ListResources("machine", o)
	if err != nil {
		return nil, 0, err
	}

	machines := make([]*Machine, 0, len(mds))
	for i := range mds {
		var spec MachineSpec
		var status MachineStatus
		_ = json.Unmarshal(specs[i], &spec)
		_ = json.Unmarshal(statuses[i], &status)
		machines = append(machines, &Machine{Metadata: mds[i], Spec: spec, Status: status})
	}

	return machines, total, nil
}

// ListMachinesByTenant returns machines for a specific tenant.
func (s *Store) ListMachinesByTenant(tenantName string, opts ...ListOption) ([]*Machine, int, error) {
	opts = append(opts, WithLabelSelector("rezuscloud.io/tenant="+tenantName))
	return s.ListMachines(opts...)
}

// UpdateMachineStatus updates a machine's status.
func (s *Store) UpdateMachineStatus(id string, status MachineStatus) (*Machine, error) {
	md, err := s.UpdateStatus("machine", id, status)
	if err != nil {
		return nil, err
	}

	var spec MachineSpec
	_, _ = s.GetResource("machine", id, &spec, nil)

	return &Machine{Metadata: md, Spec: spec, Status: status}, nil
}

// DeleteMachine sets deletionTimestamp on a machine.
func (s *Store) DeleteMachine(id string) error {
	_, err := s.DeleteResource("machine", id)
	if err != nil {
		return err
	}

	_ = s.AddFinalizer("machine", id, "rezuscloud.io/config")
	_ = s.AddFinalizer("machine", id, "rezuscloud.io/link")

	return nil
}

// --- Provider-Specific Operations ---

// UpsertProvider creates or updates a provider.
func (s *Store) UpsertProvider(providerType string, spec ProviderSpec, status ProviderStatus, labels map[string]string) (*Provider, error) {
	existing, _ := s.GetProvider(providerType)
	if existing != nil {
		_, err := s.UpdateStatus("provider", providerType, status)
		if err != nil {
			return nil, err
		}
		existing.Status = status
		return existing, nil
	}

	if labels == nil {
		labels = map[string]string{}
	}

	md, err := s.CreateResource("provider", providerType, spec, status, labels, nil)
	if err != nil {
		return nil, err
	}

	return &Provider{Metadata: md, Spec: spec, Status: status}, nil
}

// GetProvider returns a provider by type.
func (s *Store) GetProvider(providerType string) (*Provider, error) {
	var spec ProviderSpec
	var status ProviderStatus

	md, err := s.GetResource("provider", providerType, &spec, &status)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &Provider{Metadata: md, Spec: spec, Status: status}, nil
}

// ListProviders returns all providers.
func (s *Store) ListProviders() ([]*Provider, error) {
	mds, specs, statuses, _, err := s.ListResources("provider", ListOptions{})
	if err != nil {
		return nil, err
	}

	providers := make([]*Provider, 0, len(mds))
	for i := range mds {
		var spec ProviderSpec
		var status ProviderStatus
		_ = json.Unmarshal(specs[i], &spec)
		_ = json.Unmarshal(statuses[i], &status)
		providers = append(providers, &Provider{Metadata: mds[i], Spec: spec, Status: status})
	}

	return providers, nil
}

// --- JoinToken-Specific Operations ---

// ListJoinTokens returns all join tokens. Use ListOption label selectors
// to filter by tenant or node group.
func (s *Store) ListJoinTokens(opts ...ListOption) ([]*JoinToken, int, error) {
	o := newListOptions(opts...)
	mds, specs, statuses, total, err := s.ListResources("jointoken", o)
	if err != nil {
		return nil, 0, err
	}

	items := make([]*JoinToken, 0, len(mds))
	for i := range mds {
		var spec JoinTokenSpec
		var status JoinTokenStatus
		_ = json.Unmarshal(specs[i], &spec)
		_ = json.Unmarshal(statuses[i], &status)
		items = append(items, &JoinToken{
			Metadata: mds[i],
			Spec:     spec,
			Status:   status,
		})
	}

	return items, total, nil
}

// ListJoinTokensByTenant returns join tokens for a specific tenant.
func (s *Store) ListJoinTokensByTenant(tenantName string, opts ...ListOption) ([]*JoinToken, int, error) {
	opts = append(opts, WithLabelSelector("rezuscloud.io/tenant="+tenantName))
	return s.ListJoinTokens(opts...)
}

// GetJoinToken returns a join token by its value. Returns nil if not found.
// Unlike LookupJoinToken, this does not auto-remove expired tokens.
func (s *Store) GetJoinToken(token string) (*JoinToken, error) {
	var spec JoinTokenSpec
	var status JoinTokenStatus

	md, err := s.GetResource("jointoken", token, &spec, &status)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &JoinToken{Metadata: md, Spec: spec, Status: status}, nil
}

// DeleteJoinToken removes a join token from the store.
func (s *Store) DeleteJoinToken(token string) error {
	return s.RemoveResource("jointoken", token)
}

// CreateJoinToken creates a join token.
func (s *Store) CreateJoinToken(token string, spec JoinTokenSpec, tenantName, nodeGroup string) (*JoinToken, error) {
	labels := map[string]string{
		"rezuscloud.io/tenant":     tenantName,
		"rezuscloud.io/node-group": nodeGroup,
	}

	status := JoinTokenStatus{}

	md, err := s.CreateResource("jointoken", token, spec, status, labels, nil)
	if err != nil {
		return nil, err
	}

	return &JoinToken{Metadata: md, Spec: spec, Status: status}, nil
}

// LookupJoinToken finds a token by value.
func (s *Store) LookupJoinToken(token string) (*JoinToken, error) {
	var spec JoinTokenSpec
	var status JoinTokenStatus

	md, err := s.GetResource("jointoken", token, &spec, &status)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Check expiry.
	if !spec.ExpiresAt.IsZero() && time.Now().UTC().After(spec.ExpiresAt) {
		_ = s.RemoveResource("jointoken", token)
		return nil, nil
	}

	return &JoinToken{Metadata: md, Spec: spec, Status: status}, nil
}

// ConsumeJoinToken looks up and deletes a single-use token.
func (s *Store) ConsumeJoinToken(token string) (*JoinToken, error) {
	jt, err := s.LookupJoinToken(token)
	if err != nil {
		return nil, err
	}
	if jt == nil {
		return nil, nil
	}

	if jt.Spec.SingleUse {
		_ = s.RemoveResource("jointoken", token)
	}

	now := time.Now().UTC()
	jt.Status.Used = true
	jt.Status.UsedAt = &now

	return jt, nil
}

// CleanupExpiredTokens removes expired join tokens.
func (s *Store) CleanupExpiredTokens() (int, error) {
	result, err := s.db.Exec(
		`DELETE FROM resources WHERE type = 'jointoken' AND json_extract(spec, '$.expiresAt') IS NOT NULL AND json_extract(spec, '$.expiresAt') != '' AND datetime(json_extract(spec, '$.expiresAt')) < datetime('now')`,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// --- User Operations ---

// CreateUser creates a user resource.
func (s *Store) CreateUser(name string, spec UserSpec) (*User, error) {
	status := UserStatus{}
	md, err := s.CreateResource("user", name, spec, status, map[string]string{}, nil)
	if err != nil {
		return nil, err
	}
	return &User{Metadata: md, Spec: spec, Status: status}, nil
}

// GetUser returns a user by name.
func (s *Store) GetUser(name string) (*User, error) {
	var spec UserSpec
	var status UserStatus
	md, err := s.GetResource("user", name, &spec, &status)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &User{Metadata: md, Spec: spec, Status: status}, nil
}

// ListUsers returns all users.
func (s *Store) ListUsers() ([]*User, error) {
	mds, specs, statuses, _, err := s.ListResources("user", ListOptions{})
	if err != nil {
		return nil, err
	}

	users := make([]*User, 0, len(mds))
	for i := range mds {
		var spec UserSpec
		var status UserStatus
		_ = json.Unmarshal(specs[i], &spec)
		_ = json.Unmarshal(statuses[i], &status)
		users = append(users, &User{Metadata: mds[i], Spec: spec, Status: status})
	}

	return users, nil
}

// UpdateUser updates a user's spec.
func (s *Store) UpdateUser(name string, currentVersion int64, spec UserSpec) (*User, error) {
	md, err := s.UpdateResource("user", name, currentVersion, spec, nil, nil)
	if err != nil {
		return nil, err
	}

	var status UserStatus
	_, _ = s.GetResource("user", name, nil, &status)

	return &User{Metadata: md, Spec: spec, Status: status}, nil
}

// DeleteUser deletes a user.
func (s *Store) DeleteUser(name string) error {
	return s.RemoveResource("user", name)
}

// UpdateUserLastLogin updates the last login timestamp for a user.
func (s *Store) UpdateUserLastLogin(name string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE resources SET status = json_set(status, '$.lastLogin', ?) WHERE type = 'user' AND name = ?`,
		now.Format(time.RFC3339), name,
	)
	return err
}

// --- API Token Operations ---

// APIToken represents a long-lived API token that authenticates as a user.
//
// The token value is never stored in plaintext: only the SHA-256 hash is
// persisted (token_hash column). The plaintext is returned to the caller
// exactly once at creation time.
type APIToken struct {
	ID        string     `json:"id"`
	UserName  string     `json:"userName"`
	Role      string     `json:"role"` // denormalized from user at lookup time
	TokenHash string     `json:"-"`    // never serialized to clients
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

// CreateAPIToken persists a new API token row.
// `id` is the public identifier (used in URLs / DELETE), `tokenHash` is the
// SHA-256 hex digest of the secret. The plaintext secret is NEVER stored.
func (s *Store) CreateAPIToken(id, userName, tokenHash string, expiresAt *time.Time) (*APIToken, error) {
	if _, err := s.db.Exec(
		`INSERT INTO api_tokens (id, user_name, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, userName, tokenHash, expiresAt, time.Now().UTC(),
	); err != nil {
		return nil, fmt.Errorf("insert api token: %w", err)
	}
	tok, err := s.GetAPIToken(id)
	if err != nil {
		return nil, err
	}
	return tok, nil
}

// GetAPIToken returns a single API token by id (without hash on response path).
func (s *Store) GetAPIToken(id string) (*APIToken, error) {
	row := s.db.QueryRow(
		`SELECT id, user_name, token_hash, expires_at, created_at, last_used FROM api_tokens WHERE id = ?`,
		id,
	)
	var tok APIToken
	var hash string
	var expires, last sql.NullTime
	if err := row.Scan(&tok.ID, &tok.UserName, &hash, &expires, &tok.CreatedAt, &last); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get api token: %w", err)
	}
	tok.TokenHash = hash
	if expires.Valid {
		t := expires.Time
		tok.ExpiresAt = &t
	}
	if last.Valid {
		t := last.Time
		tok.LastUsed = &t
	}
	return &tok, nil
}

// ListAPITokens returns API tokens, optionally filtered by owner.
func (s *Store) ListAPITokens(userName string) ([]*APIToken, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if userName == "" {
		rows, err = s.db.Query(
			`SELECT id, user_name, token_hash, expires_at, created_at, last_used FROM api_tokens ORDER BY created_at DESC`,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, user_name, token_hash, expires_at, created_at, last_used FROM api_tokens WHERE user_name = ? ORDER BY created_at DESC`,
			userName,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*APIToken
	for rows.Next() {
		var tok APIToken
		var hash string
		var expires, last sql.NullTime
		if err := rows.Scan(&tok.ID, &tok.UserName, &hash, &expires, &tok.CreatedAt, &last); err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		tok.TokenHash = hash
		if expires.Valid {
			t := expires.Time
			tok.ExpiresAt = &t
		}
		if last.Valid {
			t := last.Time
			tok.LastUsed = &t
		}
		out = append(out, &tok)
	}
	return out, rows.Err()
}

// LookupAPITokenByHash returns the token (including hash for verification) by
// matching the supplied hash digest. Used by the auth middleware during Bearer
// validation. The returned record includes the user_name so the middleware can
// re-resolve the user's role without an extra round-trip.
func (s *Store) LookupAPITokenByHash(tokenHash string) (*APIToken, error) {
	row := s.db.QueryRow(
		`SELECT id, user_name, token_hash, expires_at, created_at, last_used FROM api_tokens WHERE token_hash = ?`,
		tokenHash,
	)
	var tok APIToken
	var expires, last sql.NullTime
	if err := row.Scan(&tok.ID, &tok.UserName, &tok.TokenHash, &expires, &tok.CreatedAt, &last); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup api token: %w", err)
	}
	if expires.Valid {
		t := expires.Time
		tok.ExpiresAt = &t
	}
	if last.Valid {
		t := last.Time
		tok.LastUsed = &t
	}
	return &tok, nil
}

// TouchAPIToken records the last_used timestamp for a token.
func (s *Store) TouchAPIToken(id string) error {
	_, err := s.db.Exec(`UPDATE api_tokens SET last_used = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// DeleteAPIToken removes a token by id. Caller should validate ownership
// (only the owner or an admin may revoke).
func (s *Store) DeleteAPIToken(id string) error {
	res, err := s.db.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Helpers ---

// newUID generates a new unique identifier.
func newUID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// byteIndex returns the index of b in s, or -1.
func byteIndex(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// jsonPathKey formats a key for SQLite json_extract.
// Keys containing dots need quoting: $."key.with.dots"
func jsonPathKey(key string) string {
	if byteIndex(key, '.') >= 0 {
		return fmt.Sprintf(`"%s"`, key)
	}
	return key
}
