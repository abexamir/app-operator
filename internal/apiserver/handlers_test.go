package apiserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

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
	return New(client, logr.Discard())
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
