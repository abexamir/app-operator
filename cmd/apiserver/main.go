package main

import (
	"flag"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
	"github.com/abexamir/app-operator/internal/apiserver"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appdefinitionv1.AddToScheme(scheme))
}

func main() {
	var addr string
	flag.StringVar(&addr, "bind-address", ":8080", "Address the API server listens on.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg := ctrl.GetConfigOrDie()
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create Kubernetes client")
		os.Exit(1)
	}

	srv := apiserver.New(k8sClient, ctrl.Log.WithName("apiserver"))
	setupLog.Info("starting API server", "address", addr)
	if err := srv.Run(addr); err != nil {
		setupLog.Error(err, "API server stopped")
		os.Exit(1)
	}
}
