package apiserver

import (
	"context"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestKubernetesAccessReviewer(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		request := action.(clienttesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		if request.Spec.Token != "valid-token" {
			t.Fatalf("unexpected token %q", request.Spec.Token)
		}
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true,
			User: authenticationv1.UserInfo{
				Username: "alice", UID: "user-1", Groups: []string{"developers"},
			},
		}}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		request := action.(clienttesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		attributes := request.Spec.ResourceAttributes
		if request.Spec.User != "alice" || attributes.Namespace != "team-a" || attributes.Verb != "update" || attributes.Name != "demo" {
			t.Fatalf("unexpected SubjectAccessReview: %#v", request.Spec)
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{
			Allowed: true, Reason: "allowed by test RBAC",
		}}, nil
	})

	reviewer := NewKubernetesAccessReviewer(client)
	user, authenticated, err := reviewer.Authenticate(context.Background(), "valid-token")
	if err != nil || !authenticated || user.Name != "alice" {
		t.Fatalf("unexpected authentication result: user=%#v authenticated=%v err=%v", user, authenticated, err)
	}
	allowed, reason, err := reviewer.Authorize(context.Background(), user, AccessAttributes{
		Verb: "update", Namespace: "team-a", Name: "demo",
	})
	if err != nil || !allowed || reason != "allowed by test RBAC" {
		t.Fatalf("unexpected authorization result: allowed=%v reason=%q err=%v", allowed, reason, err)
	}
}
