package apiserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

const (
	maxRequestBodyBytes = 1 << 20 // 1 MiB
	defaultLogTailLines = 500
)

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	list := &appdefinitionv1.AppDefinitionList{}
	if err := s.client.List(r.Context(), list, &client.ListOptions{Limit: 1}); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) listAppDefinitions(w http.ResponseWriter, r *http.Request) {
	list := &appdefinitionv1.AppDefinitionList{}
	if err := s.client.List(r.Context(), list); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sanitizeAppDefinitionList(list))
}

func (s *Server) listAppDefinitionsInNamespace(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	list := &appdefinitionv1.AppDefinitionList{}
	if err := s.client.List(r.Context(), list, client.InNamespace(ns)); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sanitizeAppDefinitionList(list))
}

func (s *Server) getAppDefinition(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	app := &appdefinitionv1.AppDefinition{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, app); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, sanitizeAppDefinition(app))
}

// podSelectorLabels mirrors internal/controller/helpers.go's selectorLabels — the label the
// controller puts on every pod template it owns for an AppDefinition.
func podSelectorLabels(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name}
}

func (s *Server) getAppDefinitionLogs(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if s.clientset == nil {
		s.writeError(w, http.StatusServiceUnavailable, errors.New("log streaming is not configured"))
		return
	}

	app := &appdefinitionv1.AppDefinition{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, app); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}

	pods := &corev1.PodList{}
	if err := s.client.List(r.Context(), pods, client.InNamespace(ns), client.MatchingLabels(podSelectorLabels(name))); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(pods.Items) == 0 {
		s.writeError(w, http.StatusNotFound, errors.New("no pods found for this app"))
		return
	}

	podName := r.URL.Query().Get("pod")
	if podName == "" {
		podName = pods.Items[0].Name
	} else {
		found := false
		for _, pod := range pods.Items {
			if pod.Name == podName {
				found = true
				break
			}
		}
		if !found {
			s.writeError(w, http.StatusNotFound, fmt.Errorf("pod %q does not belong to app %q", podName, name))
			return
		}
	}

	tailLines := int64(defaultLogTailLines)
	opts := &corev1.PodLogOptions{
		Container: r.URL.Query().Get("container"),
		Follow:    r.URL.Query().Get("follow") == "true",
		Previous:  r.URL.Query().Get("previous") == "true",
		TailLines: &tailLines,
	}
	if raw := r.URL.Query().Get("tailLines"); raw != "" {
		tail, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || tail < 0 {
			s.writeError(w, http.StatusBadRequest, errors.New("tailLines must be a non-negative integer"))
			return
		}
		opts.TailLines = &tail
	}

	stream, err := s.clientset.CoreV1().Pods(ns).GetLogs(podName, opts).Stream(r.Context())
	if err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	defer func() { _ = stream.Close() }()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				s.log.Error(readErr, "error streaming pod logs", "namespace", ns, "pod", podName)
			}
			return
		}
	}
}

func (s *Server) createAppDefinition(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "namespace")

	app := &appdefinitionv1.AppDefinition{}
	if err := decodeJSONBody(w, r, app); err != nil {
		s.writeError(w, httpStatusForDecodeError(err), err)
		return
	}
	if err := rejectInlineSecretData(app); err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := requireSecretSources(app); err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, err)
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
	s.writeJSON(w, http.StatusCreated, sanitizeAppDefinition(app))
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
	if err := rejectInlineSecretData(update); err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	preserveInlineSecretData(existing, update)
	if err := requireSecretSources(update); err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	existing.Spec = update.Spec

	if err := s.client.Update(r.Context(), existing); err != nil {
		s.writeError(w, httpStatusFor(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, sanitizeAppDefinition(existing))
}

func rejectInlineSecretData(app *appdefinitionv1.AppDefinition) error {
	for _, secret := range app.Spec.Secrets {
		if secret.Data != nil {
			return fmt.Errorf("secrets[%s].data cannot be submitted through the API; use secretRef or externalSecrets", secret.Name)
		}
	}
	return nil
}

func requireSecretSources(app *appdefinitionv1.AppDefinition) error {
	for _, secret := range app.Spec.Secrets {
		if secret.SecretRef == "" && secret.Data == nil {
			return fmt.Errorf("secrets[%s] must set secretRef; use externalSecrets for externally managed values", secret.Name)
		}
	}
	return nil
}

func preserveInlineSecretData(existing, update *appdefinitionv1.AppDefinition) {
	existingData := make(map[string]map[string]string, len(existing.Spec.Secrets))
	for _, secret := range existing.Spec.Secrets {
		if secret.SecretRef == "" && secret.Data != nil {
			existingData[secret.Name] = secret.Data
		}
	}
	for i := range update.Spec.Secrets {
		secret := &update.Spec.Secrets[i]
		if secret.SecretRef == "" && secret.Data == nil {
			secret.Data = existingData[secret.Name]
		}
	}
}

func sanitizeAppDefinition(app *appdefinitionv1.AppDefinition) *appdefinitionv1.AppDefinition {
	sanitized := app.DeepCopy()
	for i := range sanitized.Spec.Secrets {
		sanitized.Spec.Secrets[i].Data = nil
	}
	return sanitized
}

func sanitizeAppDefinitionList(list *appdefinitionv1.AppDefinitionList) *appdefinitionv1.AppDefinitionList {
	sanitized := list.DeepCopy()
	for i := range sanitized.Items {
		for j := range sanitized.Items[i].Spec.Secrets {
			sanitized.Items[i].Spec.Secrets[j].Data = nil
		}
	}
	return sanitized
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
	message := http.StatusText(status)
	switch status {
	case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity, http.StatusPreconditionRequired, http.StatusTooManyRequests:
		message = err.Error()
	}
	s.writeJSON(w, status, errorResponse{Error: message})
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
