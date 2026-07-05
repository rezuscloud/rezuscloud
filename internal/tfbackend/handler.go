package tfbackend

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// Handler implements the OpenTofu/Terraform HTTP backend protocol over a Store.
// Register it on a ServeMux for the methods GET, POST, DELETE, LOCK, UNLOCK.
//
// The workspace/tenant name is read from the `?ID=` query parameter that tofu
// appends to the configured address.
type Handler struct {
	store *Store
}

// NewHandler wraps a Store in an HTTP handler.
func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

// RegisterRoutes wires every backend method onto the given path on the mux.
// Go 1.22+ ServeMux accepts arbitrary method tokens, including LOCK/UNLOCK.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, path string) {
	mux.HandleFunc("GET "+path, h.handleRead)
	mux.HandleFunc("POST "+path, h.handleWrite)
	mux.HandleFunc("DELETE "+path, h.handleDelete)
	mux.HandleFunc("LOCK "+path, h.handleLock)
	mux.HandleFunc("UNLOCK "+path, h.handleUnlock)
}

// ServeHTTP dispatches by method so the handler can also be mounted directly
// (e.g. behind a wrapper that routes "/tfstate" without per-method patterns).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleRead(w, r)
	case http.MethodPost:
		h.handleWrite(w, r)
	case http.MethodDelete:
		h.handleDelete(w, r)
	case "LOCK":
		h.handleLock(w, r)
	case "UNLOCK":
		h.handleUnlock(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE, LOCK, UNLOCK")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// workspaceID reads the ?ID= query value. tofu defaults the workspace to
// "default" when the caller does not configure one; we fall back identically.
func workspaceID(r *http.Request) string {
	if id := r.URL.Query().Get("ID"); id != "" {
		return id
	}
	return "default"
}

func (h *Handler) handleRead(w http.ResponseWriter, r *http.Request) {
	id := workspaceID(r)
	state, found, err := h.store.GetState(r.Context(), id)
	if err != nil {
		slog.Error("tfbackend: get state failed", "id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		// No state yet — tofu treats a 404 as "first run".
		http.Error(w, "no state", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(state); err != nil {
		slog.Error("tfbackend: write state failed", "id", id, "err", err)
	}
}

func (h *Handler) handleWrite(w http.ResponseWriter, r *http.Request) {
	id := workspaceID(r)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStateBytes))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.PutState(r.Context(), id, body); err != nil {
		slog.Error("tfbackend: put state failed", "id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := workspaceID(r)
	found, err := h.store.DeleteState(r.Context(), id)
	if err != nil {
		slog.Error("tfbackend: delete state failed", "id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no state", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) {
	id := workspaceID(r)
	var info LockInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		http.Error(w, "invalid lock info: "+err.Error(), http.StatusBadRequest)
		return
	}
	if info.ID == "" {
		http.Error(w, "lock info missing ID", http.StatusBadRequest)
		return
	}
	holder, err := h.store.Lock(r.Context(), id, info)
	if err == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if errors.Is(err, ErrLockHeld) && holder != nil {
		// Conflict: return the existing holder's info so the client can report
		// who holds the lock (terraform expects this in the 423 body).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusLocked)
		_ = json.NewEncoder(w).Encode(holder)
		return
	}
	slog.Error("tfbackend: lock failed", "id", id, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) {
	id := workspaceID(r)
	// tofu sends {"ID": "<lockid>"} on unlock.
	var payload struct {
		ID string `json:"ID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid unlock body: "+err.Error(), http.StatusBadRequest)
		return
	}
	released, mismatch, err := h.store.Unlock(r.Context(), id, payload.ID)
	if err != nil {
		slog.Error("tfbackend: unlock failed", "id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch {
	case released:
		w.WriteHeader(http.StatusOK)
	case mismatch:
		http.Error(w, "lock ID mismatch", http.StatusConflict)
	default:
		// Nothing to unlock — tolerate it (idempotent).
		w.WriteHeader(http.StatusOK)
	}
}

// maxStateBytes caps an incoming state payload. State blobs are small (tens of
// KB typically); 16 MiB is a generous ceiling that still bounds abuse.
const maxStateBytes = 16 << 20
