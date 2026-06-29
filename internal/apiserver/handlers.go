package apiserver

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

func (s *Server) listAppDefinitions(w http.ResponseWriter, r *http.Request) {
	list := &appdefinitionv1.AppDefinitionList{}
	if err := s.client.List(r.Context(), list); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) listAppDefinitionsInNamespace(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	list := &appdefinitionv1.AppDefinitionList{}
	if err := s.client.List(r.Context(), list, client.InNamespace(ns)); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) getAppDefinition(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	app := &appdefinitionv1.AppDefinition{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, app); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, app)
}

func (s *Server) createAppDefinition(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")

	app := &appdefinitionv1.AppDefinition{}
	if err := json.NewDecoder(r.Body).Decode(app); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.Namespace = ns

	if err := s.client.Create(r.Context(), app); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	s.writeJSON(w, http.StatusCreated, app)
}

func (s *Server) updateAppDefinition(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	existing := &appdefinitionv1.AppDefinition{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, existing); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}

	update := &appdefinitionv1.AppDefinition{}
	if err := json.NewDecoder(r.Body).Decode(update); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	existing.Spec = update.Spec

	if err := s.client.Update(r.Context(), existing); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, existing)
}

func (s *Server) deleteAppDefinition(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	app := &appdefinitionv1.AppDefinition{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, app); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	if err := s.client.Delete(r.Context(), app); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error(err, "failed to encode response")
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

func (s *Server) writeError(w http.ResponseWriter, status int, err error) {
	s.log.Error(err, "request error", "status", status)
	s.writeJSON(w, status, errorResponse{Error: err.Error()})
}

func httpStatusFor(err error) int {
	switch {
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsAlreadyExists(err):
		return http.StatusConflict
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	case apierrors.IsUnauthorized(err):
		return http.StatusUnauthorized
	case apierrors.IsBadRequest(err):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
