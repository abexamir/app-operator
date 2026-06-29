# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```sh
# Controller
make test          # unit tests with envtest (real API server, no mocks)
make lint          # golangci-lint; use make lint-fix to auto-fix
make build         # builds bin/manager; also runs generate + manifests + fmt + vet
make run           # run controller locally against current kubeconfig

# API server
make build-apiserver   # builds bin/apiserver
make run-apiserver     # run API server locally (binds :8080)

# UI (from ui/)
npm run dev        # Vite dev server with HMR
npm run build      # tsc + vite build → dist/
npm run lint       # oxlint

# Code generation — run after editing api/v1/appdefinition_types.go
make generate      # regenerates zz_generated.deepcopy.go
make manifests     # regenerates CRD YAML and RBAC from kubebuilder markers

# Deployment (OrbStack / any cluster)
make install       # apply CRDs only
make deploy IMG=controller:develop
make deploy-apiserver APISERVER_IMG=apiserver:develop
make deploy-ui UI_IMG=ui:develop

# Docker images
make docker-build IMG=...
make docker-build-apiserver APISERVER_IMG=...
make docker-build-ui UI_IMG=...
```

When downloading Go modules fails with 403 from `proxy.golang.org`, use `GOPROXY=direct go mod tidy`.

For locally built Docker images on OrbStack, the Kustomize manifests use `imagePullPolicy: IfNotPresent` so the cluster uses the local image without a registry push.

## Architecture

Three separate binaries and Deployments in the `appoperator-system` namespace:

```
AppDefinition CR (appdefinition.abexamir.me/v1)
        │
        ▼
  controller (cmd/main.go)              — Kubebuilder Manager, reconciles CRDs
        │
        ├── ConfigMaps → Secrets → ExternalSecrets → Deployment → Service
        ├── PVC (if spec.disk) → Ingress (if spec.domains)
        ├── HPA (if spec.autoscaling.enabled) → ServiceMonitor (if metrics enabled)
        └── status subresource (phase, readyReplicas, per-resource conditions)

  apiserver (cmd/apiserver/main.go)     — BFF; standalone controller-runtime client
        │                                 chi router, no Manager
        └── /api/v1/appdefinitions      — CRUD proxied to kube API server

  UI (ui/)                              — React + Vite + MUI v6 dark theme
        │                                 nginx serves static build; /api/* proxied
        └── → apiserver via cluster DNS  to app-operator-apiserver.appoperator-system.svc
```

### Controller reconcile loop (`internal/controller/`)

`appdefinition_controller.go` is the entry point. `reconcileAll` calls sub-functions in this fixed order (order matters — ConfigMaps/Secrets must exist before Deployment):

1. `reconcileConfigMaps` → `reconcileSecrets` → `reconcileExternalSecrets`
2. `reconcileDeployment` → `reconcileService`
3. `reconcilePVC` (only when `spec.disk` is set)
4. `reconcileIngress` (only when `spec.domains` is non-empty)
5. `reconcileHPA` → `reconcileServiceMonitor`
6. `updateStatus`

All resources use `ctrl.CreateOrUpdate` — the loop is fully idempotent. A 30-second periodic requeue corrects drift between event-driven triggers.

**Config hash rollout**: when inline `configMaps[].data` or `secrets[].data` changes, a SHA-256 hash is stored in pod template annotation `appdefinition.abexamir.me/config-hash`, triggering a rolling restart automatically.

**Stateful apps** (`spec.disk` set): forced to `Recreate` strategy, max 1 replica, HPA disabled. These constraints are also enforced by CRD CEL validation rules — no admission webhook needed.

**Lifecycle hooks**: applied to `containers[0]` only. Sidecars are skipped to avoid exec-hook crash loops.

**`loggingConfig`**: stored in spec, not acted on by the operator — log shipping is handled at the cluster layer.

### Optional integrations (gracefully skipped when CRDs absent)

Both `ServiceMonitor` (prometheus-operator) and `ExternalSecret` (external-secrets.io) are managed via `unstructured.Unstructured` to avoid hard Go dependencies. When their CRDs are absent, `apimeta.IsNoMatchError` is caught and the step is silently skipped.

`ExternalSecret` existence checks use `r.APIReader` (bypasses the informer cache) rather than `r.Get` because the cached client only has informers for scheme-registered types.

### API server (`internal/apiserver/`)

`server.go` builds a chi router with CORS and logging middleware. `handlers.go` contains all CRUD handlers. The server uses a standalone `controller-runtime` client built without a Manager — it connects to the Kubernetes API directly using in-cluster config (or local kubeconfig when run locally).

Routes:
- `GET /healthz`
- `GET /api/v1/appdefinitions` (all namespaces)
- `GET|POST /api/v1/namespaces/{namespace}/appdefinitions`
- `GET|PUT|DELETE /api/v1/namespaces/{namespace}/appdefinitions/{name}`

### UI (`ui/src/`)

- `theme.ts` — MUI v6 dark theme (primary `#7c6af7`, background `#0d0e14`)
- `types/appdefinition.ts` — TypeScript types mirroring the Go API types
- `api/` — TanStack Query v5 wrappers; 10-second auto-refresh on list
- `pages/AppList.tsx` — MUI DataGrid with per-row view/edit/delete
- `pages/AppDetail.tsx` — 7-tab read-only view of all spec fields
- `pages/AppForm.tsx` — 8-tab react-hook-form editor covering all spec fields; `useFieldArray` for dynamic arrays (ports, env, containers, etc.)

### RBAC

Controller RBAC is generated from `// +kubebuilder:rbac:...` markers in `appdefinition_controller.go`. Run `make manifests` after changing markers to regenerate `config/rbac/role.yaml`. The API server has its own `ServiceAccount` and `ClusterRole` in `config/apiserver/rbac.yaml` — edit that file directly (no marker generation).

### CRD CEL validation rules

Three rules are baked into the CRD schema (no webhook needed):
- `disk + replicas > 1` → rejected (ReadWriteOnce PVCs, single writer)
- `disk + autoscaling.enabled` → rejected
- `secrets[].data + secrets[].secretRef` → rejected (mutually exclusive)
