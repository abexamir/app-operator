package controller

import (
	"fmt"
	"strings"

	v1 "github.com/abexamir/app-operator/api/v1"
)

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
