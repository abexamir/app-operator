package apiserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

const maxRequestBodyBytes = 1 << 20 // 1 MiB

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
	if err := decodeJSONBody(w, r, app); err != nil {
		s.writeError(w, httpStatusForDecodeError(err), err)
		return
	}
	app.ResourceVersion = ""
	app.UID = ""
	app.Generation = 0
	app.ManagedFields = nil
	app.CreationTimestamp = metav1.Time{}
	app.DeletionTimestamp = nil
	app.DeletionGracePeriodSeconds = nil
	app.Finalizers = nil
	app.Namespace = ns
	app.Status = appdefinitionv1.AppDefinitionStatus{}

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
	if err := decodeJSONBody(w, r, update); err != nil {
		s.writeError(w, httpStatusForDecodeError(err), err)
		return
	}
	if update.Name != "" && update.Name != name {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("metadata.name must match URL name %q", name))
		return
	}
	if update.Namespace != "" && update.Namespace != ns {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("metadata.namespace must match URL namespace %q", ns))
		return
	}
	if update.ResourceVersion == "" {
		s.writeError(w, http.StatusPreconditionRequired, errors.New("metadata.resourceVersion is required for updates"))
		return
	}
	if update.ResourceVersion != existing.ResourceVersion {
		s.writeError(w, http.StatusConflict, errors.New("resource was modified; refresh and retry"))
		return
	}
	existing.Spec = update.Spec

	if err := s.client.Update(r.Context(), existing); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, existing)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON object")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func httpStatusForDecodeError(err error) int {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
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
	case apierrors.IsInvalid(err):
		return http.StatusUnprocessableEntity
	case apierrors.IsConflict(err):
		return http.StatusConflict
	case apierrors.IsRequestEntityTooLargeError(err):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}
