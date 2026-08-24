package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

type staticAccessReviewer struct {
	authenticated bool
	allowed       bool
	attributes    []AccessAttributes
}

func (r *staticAccessReviewer) Authenticate(_ context.Context, _ string) (AuthenticatedUser, bool, error) {
	return AuthenticatedUser{Name: "test-user", Groups: []string{"developers"}}, r.authenticated, nil
}

func (r *staticAccessReviewer) Authorize(_ context.Context, _ AuthenticatedUser, attributes AccessAttributes) (bool, string, error) {
	r.attributes = append(r.attributes, attributes)
	return r.allowed, "test decision", nil
}

func newTestServer(t *testing.T, objects ...runtime.Object) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appdefinitionv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return New(client, logr.Discard(), WithAccessReviewer(&staticAccessReviewer{authenticated: true, allowed: true}))
}

func validApp(resourceVersion string) appdefinitionv1.AppDefinition {
	return appdefinitionv1.AppDefinition{
		TypeMeta: metav1.TypeMeta{APIVersion: appdefinitionv1.GroupVersion.String(), Kind: "AppDefinition"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "demo",
			Namespace:       "apps",
			ResourceVersion: resourceVersion,
		},
		Spec: appdefinitionv1.AppDefinitionSpec{
			Containers: []appdefinitionv1.ContainerSpec{{Name: "app", Image: "example/app:v1"}},
		},
	}
}

func requestJSON(t *testing.T, server *Server, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	return response
}

func TestUpdateRequiresResourceVersion(t *testing.T) {
	existing := validApp("7")
	server := newTestServer(t, &existing)
	update := validApp("")

	response := requestJSON(t, server, http.MethodPut, "/api/v1/namespaces/apps/appdefinitions/demo", update)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("expected %d, got %d: %s", http.StatusPreconditionRequired, response.Code, response.Body.String())
	}
}

func TestUpdateRejectsStaleResourceVersion(t *testing.T) {
	existing := validApp("7")
	server := newTestServer(t, &existing)
	update := validApp("6")

	response := requestJSON(t, server, http.MethodPut, "/api/v1/namespaces/apps/appdefinitions/demo", update)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d: %s", http.StatusConflict, response.Code, response.Body.String())
	}
}

func TestUpdateWithCurrentResourceVersionSucceeds(t *testing.T) {
	existing := validApp("7")
	server := newTestServer(t, &existing)
	update := validApp("7")
	update.Spec.Containers[0].Image = "example/app:v2"

	response := requestJSON(t, server, http.MethodPut, "/api/v1/namespaces/apps/appdefinitions/demo", update)
	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
}

func TestCreateRejectsUnknownFields(t *testing.T) {
	server := newTestServer(t)
	body := `{"apiVersion":"appdefinition.abexamir.me/v1","kind":"AppDefinition","metadata":{"name":"demo"},"spec":{"containers":[{"name":"app","image":"example/app:v1"}],"unknown":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/apps/appdefinitions/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, response.Code, response.Body.String())
	}
}

func TestCreateRejectsOversizedBody(t *testing.T) {
	server := newTestServer(t)
	body := `{"padding":"` + strings.Repeat("x", maxRequestBodyBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/apps/appdefinitions/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected %d, got %d: %s", http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
	}
}

func TestMetricsEndpointReportsHTTPRequests(t *testing.T) {
	server := newTestServer(t)
	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	server.Handler().ServeHTTP(httptest.NewRecorder(), healthRequest)

	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, metricsRequest)

	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
	if !strings.Contains(response.Body.String(), "appoperator_apiserver_http_requests_total") {
		t.Fatal("expected API server request metric")
	}
}

func TestAPIRequiresBearerAuthentication(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/apps/appdefinitions/", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d: %s", http.StatusUnauthorized, response.Code, response.Body.String())
	}
}

func TestAPIDeniesUnauthorizedNamespaceAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appdefinitionv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reviewer := &staticAccessReviewer{authenticated: true, allowed: false}
	server := New(fake.NewClientBuilder().WithScheme(scheme).Build(), logr.Discard(), WithAccessReviewer(reviewer))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/private/appdefinitions/demo", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d: %s", http.StatusForbidden, response.Code, response.Body.String())
	}
	if len(reviewer.attributes) != 1 || reviewer.attributes[0].Namespace != "private" || reviewer.attributes[0].Verb != "get" {
		t.Fatalf("unexpected authorization attributes: %#v", reviewer.attributes)
	}
}

func TestListRedactsInlineSecretData(t *testing.T) {
	app := validApp("7")
	app.Spec.Secrets = []appdefinitionv1.SecretMount{{Name: "legacy", Data: map[string]string{"password": "secret"}}}
	server := newTestServer(t, &app)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/apps/appdefinitions/", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "\"password\"") {
		t.Fatalf("inline secret value leaked in response: %s", response.Body.String())
	}
}

func TestCreateRejectsInlineSecretData(t *testing.T) {
	server := newTestServer(t)
	app := validApp("")
	app.Spec.Secrets = []appdefinitionv1.SecretMount{{Name: "legacy", Data: map[string]string{"password": "secret"}}}
	response := requestJSON(t, server, http.MethodPost, "/api/v1/namespaces/apps/appdefinitions/", app)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected %d, got %d: %s", http.StatusUnprocessableEntity, response.Code, response.Body.String())
	}
}

func TestUpdatePreservesLegacyInlineSecretDataWithoutReturningIt(t *testing.T) {
	existing := validApp("7")
	existing.Spec.Secrets = []appdefinitionv1.SecretMount{{Name: "legacy", Data: map[string]string{"password": "secret"}}}
	server := newTestServer(t, &existing)
	update := validApp("7")
	update.Spec.Secrets = []appdefinitionv1.SecretMount{{Name: "legacy"}}

	response := requestJSON(t, server, http.MethodPut, "/api/v1/namespaces/apps/appdefinitions/demo", update)
	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "\"password\"") {
		t.Fatalf("inline secret value leaked in response: %s", response.Body.String())
	}
	stored := &appdefinitionv1.AppDefinition{}
	if err := server.client.Get(context.Background(), types.NamespacedName{Name: "demo", Namespace: "apps"}, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Secrets[0].Data["password"] != "secret" {
		t.Fatal("legacy inline secret data was not preserved")
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/appdefinitions", nil)
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, response.Code)
	}
}

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appdefinitionv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := New(fake.NewClientBuilder().WithScheme(scheme).Build(), logr.Discard(),
		WithAccessReviewer(&staticAccessReviewer{authenticated: true, allowed: true}),
		WithAllowedOrigins("https://console.example"),
	)
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/appdefinitions", nil)
	request.Header.Set("Origin", "https://console.example")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d", http.StatusNoContent, response.Code)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example" {
		t.Fatalf("unexpected allow origin %q", got)
	}
}
