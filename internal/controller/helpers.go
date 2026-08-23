package controller

import (
	"fmt"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// resolvePreferredGVK finds the first API version of groupKind the cluster's RESTMapper
// actually recognizes, trying versions in order (newest-first by convention — see callers'
// own version-list comments for why a given CRD's list is ordered the way it is). Returns
// the same apimeta-recognizable NoMatchError the mapper itself returns when none of them
// exist, so callers' apimeta.IsNoMatchError graceful-skip handling doesn't need to change.
// Shared by every optional/unstructured CRD integration (ExternalSecret, SecretStore, ...)
// since they all hit the same "which version does this cluster actually serve" problem.
func resolvePreferredGVK(mapper apimeta.RESTMapper, groupKind schema.GroupKind, versions []string) (schema.GroupVersionKind, error) {
	var lastErr error
	for _, version := range versions {
		mapping, err := mapper.RESTMapping(groupKind, version)
		if err == nil {
			return mapping.GroupVersionKind, nil
		}
		lastErr = err
	}
	return schema.GroupVersionKind{}, lastErr
}

func standardLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "app-operator",
	}
}

func selectorLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name": name,
	}
}

func pvcName(appName string) string {
	return appName + "-disk"
}

func isStateful(appDef *v1.AppDefinition) bool {
	return appDef.Spec.Disk != nil
}

// serviceMonitorName avoids naming the ServiceMonitor after the app workload;
// some clusters prune ServiceMonitors that share the app's exact name.
func serviceMonitorName(appName string) string {
	return appName + "-monitor"
}

// resolvedSecretName returns the Kubernetes Secret name for a SecretMount.
// When SecretRef is set, the referenced secret is used directly (not managed by the operator).
func resolvedSecretName(appName string, sec v1.SecretMount) string {
	if sec.SecretRef != "" {
		return sec.SecretRef
	}
	return appName + "-" + sec.Name
}

// tlsSecretName generates a DNS-safe TLS secret name from the app name and domain.
func tlsSecretName(appName, domain string) string {
	safe := sanitizeDNS(domain)
	return fmt.Sprintf("%s-%s-tls", appName, safe)
}

func sanitizeDNS(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
