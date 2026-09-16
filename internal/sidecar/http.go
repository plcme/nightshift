package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type API struct {
	store      *Store
	controller *Controller
}

func NewAPI(store *Store, controller *Controller) http.Handler {
	api := &API{store: store, controller: controller}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", api.health)
	mux.HandleFunc("GET /api/status", api.status)
	mux.HandleFunc("POST /api/refresh", api.refresh)
	mux.HandleFunc("GET /api/tasks", api.listTasks)
	mux.HandleFunc("POST /api/tasks", api.createTask)
	mux.HandleFunc("GET /api/tasks/{id}", api.getTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", api.updateTask)
	mux.HandleFunc("GET /api/tasks/{id}/messages", api.listMessages)
	mux.HandleFunc("POST /api/tasks/{id}/replies", api.reply)
	mux.HandleFunc("POST /api/tasks/{id}/run", api.runNow)
	mux.HandleFunc("GET /api/labels", api.listLabels)
	mux.HandleFunc("POST /api/labels", api.upsertLabel)
	mux.HandleFunc("PUT /api/settings/{key}", api.setSetting)
	return cors(mux)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) status(w http.ResponseWriter, _ *http.Request) {
	quotas, refreshedAt := a.controller.Quotas()
	enabledText, _ := a.store.GetSetting("scheduler_enabled", "false")
	enabled, _ := strconv.ParseBool(enabledText)
	mainWorkText, _ := a.store.GetSetting("main_work_active", "false")
	mainWork, _ := strconv.ParseBool(mainWorkText)
	writeJSON(w, http.StatusOK, map[string]any{
		"scheduler_enabled": enabled, "main_work_active": mainWork,
		"quotas": quotas, "refreshed_at": refreshedAt,
	})
}

func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	go a.controller.Refresh(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusAccepted, map[string]any{"refreshing": true})
}

func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "true"
	tasks, err := a.store.ListTasks(includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (a *API) createTask(w http.ResponseWriter, r *http.Request) {
	var input CreateTaskInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	task, err := a.store.CreateTask(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (a *API) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.store.GetTask(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (a *API) updateTask(w http.ResponseWriter, r *http.Request) {
	var input UpdateTaskInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	task, err := a.store.UpdateTask(r.PathValue("id"), input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := a.store.ListMessages(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (a *API) reply(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	message, err := a.store.AddUserReply(r.PathValue("id"), input.Content)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, message)
}

func (a *API) runNow(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	go func() { _ = a.controller.RunTaskNow(context.WithoutCancel(r.Context()), taskID, 30*time.Minute) }()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

func (a *API) listLabels(w http.ResponseWriter, _ *http.Request) {
	labels, err := a.store.ListLabels()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
}

func (a *API) upsertLabel(w http.ResponseWriter, r *http.Request) {
	var input Label
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	label, err := a.store.UpsertLabel(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, label)
}

func (a *API) setSetting(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.PathValue("key"))
	var input struct {
		Value any `json:"value"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	data, err := json.Marshal(input.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value := string(data)
	if text, ok := input.Value.(string); ok {
		value = text
	}
	if err := a.store.SetSetting(key, value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": input.Value})
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "http://localhost:3000" || origin == "http://127.0.0.1:3000" ||
			origin == "http://localhost:3001" || origin == "http://127.0.0.1:3001" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
