package apiserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type AuthenticatedUser struct {
	Name   string
	UID    string
	Groups []string
	Extra  map[string]authenticationv1.ExtraValue
}

type AccessAttributes struct {
	Verb      string
	Namespace string
	Name      string
}

type AccessReviewer interface {
	Authenticate(ctx context.Context, bearerToken string) (AuthenticatedUser, bool, error)
	Authorize(ctx context.Context, user AuthenticatedUser, attributes AccessAttributes) (bool, string, error)
}

type KubernetesAccessReviewer struct {
	client kubernetes.Interface
}

func NewKubernetesAccessReviewer(client kubernetes.Interface) *KubernetesAccessReviewer {
	return &KubernetesAccessReviewer{client: client}
}

func (r *KubernetesAccessReviewer) Authenticate(ctx context.Context, bearerToken string) (AuthenticatedUser, bool, error) {
	review, err := r.client.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: bearerToken},
	}, metav1.CreateOptions{})
	if err != nil {
		return AuthenticatedUser{}, false, err
	}
	if !review.Status.Authenticated || review.Status.Error != "" {
		return AuthenticatedUser{}, false, nil
	}
	return AuthenticatedUser{
		Name: review.Status.User.Username, UID: review.Status.User.UID,
		Groups: review.Status.User.Groups, Extra: review.Status.User.Extra,
	}, true, nil
}

func (r *KubernetesAccessReviewer) Authorize(
	ctx context.Context,
	user AuthenticatedUser,
	attributes AccessAttributes,
) (bool, string, error) {
	extra := make(map[string]authorizationv1.ExtraValue, len(user.Extra))
	for key, values := range user.Extra {
		extra[key] = authorizationv1.ExtraValue(values)
	}
	review, err := r.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: user.Name, UID: user.UID, Groups: user.Groups, Extra: extra,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group: "appdefinition.abexamir.me", Version: "v1", Resource: "appdefinitions",
				Verb: attributes.Verb, Namespace: attributes.Namespace, Name: attributes.Name,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, "", err
	}
	return review.Status.Allowed, review.Status.Reason, nil
}

type userContextKey struct{}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.accessReviewer == nil {
			s.writeError(w, http.StatusServiceUnavailable, errors.New("API authentication is not configured"))
			return
		}
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) || strings.TrimSpace(strings.TrimPrefix(header, prefix)) == "" {
			s.writeError(w, http.StatusUnauthorized, errors.New("a bearer token is required"))
			return
		}
		user, authenticated, err := s.accessReviewer.Authenticate(r.Context(), strings.TrimSpace(strings.TrimPrefix(header, prefix)))
		if err != nil {
			s.writeError(w, http.StatusServiceUnavailable, errors.New("authentication service unavailable"))
			return
		}
		if !authenticated {
			s.writeError(w, http.StatusUnauthorized, errors.New("invalid bearer token"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	})
}

func (s *Server) requireAccess(verb string, namespaced, named bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := r.Context().Value(userContextKey{}).(AuthenticatedUser)
			if !ok {
				s.writeError(w, http.StatusUnauthorized, errors.New("authenticated user missing"))
				return
			}
			attributes := AccessAttributes{Verb: verb}
			if namespaced {
				attributes.Namespace = chi.URLParam(r, "namespace")
			}
			if named {
				attributes.Name = chi.URLParam(r, "name")
			}
			allowed, reason, err := s.accessReviewer.Authorize(r.Context(), user, attributes)
			if err != nil {
				s.writeError(w, http.StatusServiceUnavailable, errors.New("authorization service unavailable"))
				return
			}
			if !allowed {
				s.log.Info("API request denied", "user", user.Name, "verb", verb,
					"namespace", attributes.Namespace, "name", attributes.Name, "reason", reason)
				s.writeError(w, http.StatusForbidden, errors.New("forbidden by Kubernetes RBAC"))
				return
			}
			s.log.Info("API request authorized", "user", user.Name, "verb", verb,
				"namespace", attributes.Namespace, "name", attributes.Name)
			next.ServeHTTP(w, r)
		})
	}
}
