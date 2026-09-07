package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// middlewareAPI identifies which Traefik Middleware API group a cluster serves and the spec
// key its IP allow-list field uses under that group.
type middlewareAPI struct {
	gvk       schema.GroupVersionKind
	ipListKey string
}

// middlewareAPICandidates is tried in order: traefik.io is the current group (Traefik v2.10+
// and v3) using the non-deprecated "ipAllowList" field; traefik.containo.us is the legacy
// group removed in Traefik v3 but still served by older v2 clusters (e.g. zarrino-infra's
// production cluster), which only understands "ipWhiteList".
var middlewareAPICandidates = []middlewareAPI{
	{gvk: schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"}, ipListKey: "ipAllowList"},
	{gvk: schema.GroupVersionKind{Group: "traefik.containo.us", Version: "v1alpha1", Kind: "Middleware"}, ipListKey: "ipWhiteList"},
}

// resolveMiddlewareAPI finds the first Middleware API group/version the cluster's RESTMapper
// actually recognizes. Returns the same apimeta-recognizable NoMatchError the mapper itself
// returns when neither is installed, so callers' apimeta.IsNoMatchError graceful-skip handling
// works the same as it does for ServiceMonitor and ExternalSecret.
func resolveMiddlewareAPI(mapper apimeta.RESTMapper) (middlewareAPI, error) {
	var lastErr error
	for _, candidate := range middlewareAPICandidates {
		if _, err := mapper.RESTMapping(candidate.gvk.GroupKind(), candidate.gvk.Version); err == nil {
			return candidate, nil
		} else {
			lastErr = err
		}
	}
	return middlewareAPI{}, lastErr
}

// enabledMiddlewareKinds returns the configured middleware kinds for a domain, in the fixed
// chain order applied via the router.middlewares annotation (see MiddlewaresSpec's doc
// comment): IPWhiteList and RateLimit filter cheaply before the two auth mechanisms run, with
// Headers last since it only decorates the response rather than gating the request.
func enabledMiddlewareKinds(domain v1.DomainSpec) []string {
	mw := domain.Middlewares
	if mw == nil {
		return nil
	}
	var kinds []string
	if mw.IPWhiteList != nil {
		kinds = append(kinds, "ipallow")
	}
	if mw.RateLimit != nil {
		kinds = append(kinds, "ratelimit")
	}
	if mw.ForwardAuth != nil {
		kinds = append(kinds, "forwardauth")
	}
	if mw.BasicAuth != nil {
		kinds = append(kinds, "basicauth")
	}
	if mw.Headers != nil {
		kinds = append(kinds, "headers")
	}
	return kinds
}

// reconcileMiddlewares creates or updates one Traefik Middleware per configured kind on each
// domain (see enabledMiddlewareKinds), named by middlewareName. reconcileIngress references
// these same names directly when building the router.middlewares annotation, so the two
// functions must stay in sync on naming and on which kinds count as "enabled" — both go
// through enabledMiddlewareKinds and middlewareName to guarantee that.
//
// The function is a no-op when neither Traefik Middleware CRD group is installed — the same
// graceful-skip pattern used for ServiceMonitor and ExternalSecret.
func (r *AppDefinitionReconciler) reconcileMiddlewares(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	api, err := resolveMiddlewareAPI(r.RESTMapper())
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("Traefik Middleware CRD not installed, skipping")
			return nil
		}
		return fmt.Errorf("resolving Middleware API version: %w", err)
	}

	desiredNames := make(map[string]struct{})

	for _, domain := range appDef.Spec.Domains {
		for _, kind := range enabledMiddlewareKinds(domain) {
			name := middlewareName(appDef.Name, domain.Name, kind)
			desiredNames[name] = struct{}{}

			desired := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": api.gvk.GroupVersion().String(),
					"kind":       api.gvk.Kind,
					"metadata": map[string]interface{}{
						"name":      name,
						"namespace": appDef.Namespace,
						"labels":    labelsToInterface(standardLabels(appDef.Name)),
					},
					"spec": buildMiddlewareSpec(kind, domain, api),
				},
			}
			if err := ctrl.SetControllerReference(appDef, desired, r.Scheme); err != nil {
				return fmt.Errorf("setting owner reference on Middleware %s: %w", name, err)
			}

			key := types.NamespacedName{Name: name, Namespace: appDef.Namespace}
			existing := &unstructured.Unstructured{}
			existing.SetGroupVersionKind(api.gvk)

			getErr := r.APIReader.Get(ctx, key, existing)
			if getErr != nil {
				if apimeta.IsNoMatchError(getErr) {
					logger.V(1).Info("Traefik Middleware CRD not installed, skipping")
					return nil
				}
				if apierrors.IsNotFound(getErr) {
					logger.Info("creating Middleware", "name", name)
					if createErr := r.Create(ctx, desired); createErr != nil {
						if apimeta.IsNoMatchError(createErr) {
							logger.V(1).Info("Traefik Middleware CRD not installed, skipping")
							return nil
						}
						return fmt.Errorf("creating Middleware %s: %w", name, createErr)
					}
					continue
				}
				return fmt.Errorf("getting Middleware %s: %w", name, getErr)
			}

			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				if err := r.APIReader.Get(ctx, key, existing); err != nil {
					return err
				}
				desired.SetResourceVersion(existing.GetResourceVersion())
				return r.Update(ctx, desired)
			}); err != nil {
				return fmt.Errorf("updating Middleware %s: %w", name, err)
			}
			logger.Info("updating Middleware", "name", name)
		}
	}

	return r.pruneMiddlewares(ctx, appDef, api.gvk, desiredNames)
}

func (r *AppDefinitionReconciler) pruneMiddlewares(
	ctx context.Context,
	appDef *v1.AppDefinition,
	gvk schema.GroupVersionKind,
	desiredNames map[string]struct{},
) error {
	logger := log.FromContext(ctx)
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion(gvk.GroupVersion().String())
	list.SetKind(gvk.Kind + "List")
	if err := r.APIReader.List(ctx, list,
		client.InNamespace(appDef.Namespace),
		client.MatchingLabels(standardLabels(appDef.Name)),
	); err != nil {
		if apimeta.IsNoMatchError(err) {
			return nil
		}
		return fmt.Errorf("listing managed Middlewares for pruning: %w", err)
	}

	for i := range list.Items {
		obj := &list.Items[i]
		if !metav1.IsControlledBy(obj, appDef) {
			continue
		}
		if _, keep := desiredNames[obj.GetName()]; keep {
			continue
		}
		logger.Info("deleting stale Middleware", "name", obj.GetName())
		if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting stale Middleware %s: %w", obj.GetName(), err)
		}
		recordManagedResourcePrune("Middleware")
	}
	return nil
}

// buildMiddlewareSpec renders the Traefik-schema "spec" body for one middleware kind.
func buildMiddlewareSpec(kind string, domain v1.DomainSpec, api middlewareAPI) map[string]interface{} {
	mw := domain.Middlewares
	switch kind {
	case "ipallow":
		return buildIPAllowListSpec(mw.IPWhiteList, api)
	case "ratelimit":
		return buildRateLimitSpec(mw.RateLimit)
	case "forwardauth":
		return buildForwardAuthSpec(mw.ForwardAuth)
	case "basicauth":
		return buildBasicAuthSpec(mw.BasicAuth)
	case "headers":
		return buildHeadersSpec(mw.Headers)
	default:
		return nil
	}
}

func buildIPAllowListSpec(ipw *v1.IPWhiteListSpec, api middlewareAPI) map[string]interface{} {
	body := map[string]interface{}{
		"sourceRange": toInterfaceSlice(ipw.SourceRange),
	}
	if ipw.IPStrategy != nil {
		strategy := map[string]interface{}{}
		if ipw.IPStrategy.Depth != 0 {
			strategy["depth"] = int64(ipw.IPStrategy.Depth)
		}
		if len(ipw.IPStrategy.ExcludedIPs) > 0 {
			strategy["excludedIPs"] = toInterfaceSlice(ipw.IPStrategy.ExcludedIPs)
		}
		if len(strategy) > 0 {
			body["ipStrategy"] = strategy
		}
	}
	return map[string]interface{}{api.ipListKey: body}
}

func buildBasicAuthSpec(ba *v1.BasicAuthSpec) map[string]interface{} {
	body := map[string]interface{}{"secret": ba.SecretName}
	if ba.Realm != "" {
		body["realm"] = ba.Realm
	}
	if ba.RemoveHeader {
		body["removeHeader"] = true
	}
	if ba.HeaderField != "" {
		body["headerField"] = ba.HeaderField
	}
	return map[string]interface{}{"basicAuth": body}
}

func buildForwardAuthSpec(fa *v1.ForwardAuthSpec) map[string]interface{} {
	body := map[string]interface{}{"address": fa.Address}
	if fa.TrustForwardHeader {
		body["trustForwardHeader"] = true
	}
	if len(fa.AuthResponseHeaders) > 0 {
		body["authResponseHeaders"] = toInterfaceSlice(fa.AuthResponseHeaders)
	}
	if len(fa.AuthRequestHeaders) > 0 {
		body["authRequestHeaders"] = toInterfaceSlice(fa.AuthRequestHeaders)
	}
	if fa.TLSInsecureSkipVerify {
		body["tls"] = map[string]interface{}{"insecureSkipVerify": true}
	}
	return map[string]interface{}{"forwardAuth": body}
}

func buildRateLimitSpec(rl *v1.RateLimitSpec) map[string]interface{} {
	body := map[string]interface{}{"average": rl.Average}
	if rl.Burst != 0 {
		body["burst"] = rl.Burst
	}
	if rl.Period != "" {
		body["period"] = rl.Period
	}
	return map[string]interface{}{"rateLimit": body}
}

func buildHeadersSpec(h *v1.HeadersSpec) map[string]interface{} {
	body := map[string]interface{}{}
	if len(h.CustomRequestHeaders) > 0 {
		body["customRequestHeaders"] = labelsToInterface(h.CustomRequestHeaders)
	}
	if len(h.CustomResponseHeaders) > 0 {
		body["customResponseHeaders"] = labelsToInterface(h.CustomResponseHeaders)
	}
	if h.STSSeconds != 0 {
		body["stsSeconds"] = h.STSSeconds
	}
	if h.STSIncludeSubdomains {
		body["stsIncludeSubdomains"] = true
	}
	if h.STSPreload {
		body["stsPreload"] = true
	}
	if h.ForceSTSHeader {
		body["forceSTSHeader"] = true
	}
	if h.FrameDeny {
		body["frameDeny"] = true
	}
	if h.ContentTypeNosniff {
		body["contentTypeNosniff"] = true
	}
	if len(h.AccessControlAllowOriginList) > 0 {
		body["accessControlAllowOriginList"] = toInterfaceSlice(h.AccessControlAllowOriginList)
	}
	if len(h.AccessControlAllowMethods) > 0 {
		body["accessControlAllowMethods"] = toInterfaceSlice(h.AccessControlAllowMethods)
	}
	if len(h.AccessControlAllowHeaders) > 0 {
		body["accessControlAllowHeaders"] = toInterfaceSlice(h.AccessControlAllowHeaders)
	}
	if h.AccessControlAllowCredentials {
		body["accessControlAllowCredentials"] = true
	}
	return map[string]interface{}{"headers": body}
}

func toInterfaceSlice(strs []string) []interface{} {
	out := make([]interface{}, len(strs))
	for i, s := range strs {
		out[i] = s
	}
	return out
}
