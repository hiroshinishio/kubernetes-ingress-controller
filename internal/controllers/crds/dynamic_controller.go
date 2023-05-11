package crds

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/kong/kubernetes-ingress-controller/v2/internal/controllers/utils"
	"github.com/kong/kubernetes-ingress-controller/v2/internal/util"
)

// +kubebuilder:rbac:groups="apiextensions.k8s.io",resources=customresourcedefinitions,verbs=list;watch

type Controller interface {
	SetupWithManager(mgr ctrl.Manager) error
}

// DynamicController ensures that RequiredCRDs are installed in the cluster and only then sets up a Controller that
// depends on them.
// In case the CRDs are not installed at start-up time, DynamicController will set up a watch for CustomResourceDefinition
// and will dynamically set up a Controller once it detects that all RequiredCRDs are already in place.
type DynamicController struct {
	Log              logr.Logger
	Manager          ctrl.Manager
	CacheSyncTimeout time.Duration
	Controllers      []Controller
	RequiredCRDs     []schema.GroupVersionResource

	startControllersOnce sync.Once
}

func (r *DynamicController) SetupWithManager(mgr ctrl.Manager) error {
	if r.allRequiredCRDsInstalled() {
		r.Log.V(util.DebugLevel).Info("All required CustomResourceDefinitions are installed, skipping DynamicController set up")
		return r.setupControllers(mgr)
	}

	r.Log.Info("Required CustomResourceDefinitions are not installed, setting up a watch for them in case they are installed afterward")

	c, err := controller.New("DynamicController", mgr, controller.Options{
		Reconciler: r,
		LogConstructor: func(_ *reconcile.Request) logr.Logger {
			return r.Log
		},
		CacheSyncTimeout: r.CacheSyncTimeout,
	})
	if err != nil {
		return err
	}

	return c.Watch(
		&source.Kind{Type: &apiextensionsv1.CustomResourceDefinition{}},
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(r.isOneOfRequiredCRDs),
	)
}

func (r *DynamicController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("CustomResourceDefinition", req.NamespacedName)

	crd := new(apiextensionsv1.CustomResourceDefinition)
	if err := r.Manager.GetClient().Get(ctx, req.NamespacedName, crd); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(util.DebugLevel).Info("Object enqueued no longer exists, skipping", "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	log.V(util.InfoLevel).Info("Processing CustomResourceDefinition", "name", req.Name)

	if !r.allRequiredCRDsInstalled() {
		log.V(util.InfoLevel).Info("Still not all required CustomResourceDefinitions are installed, waiting")
		return ctrl.Result{}, nil
	}

	var startControllersErr error
	r.startControllersOnce.Do(func() {
		log.Info("All required CustomResourceDefinitions are installed, setting up the controllers")
		startControllersErr = r.setupControllers(r.Manager)
	})
	if startControllersErr != nil {
		return ctrl.Result{}, startControllersErr
	}

	return ctrl.Result{}, nil
}

func (r *DynamicController) allRequiredCRDsInstalled() bool {
	return lo.EveryBy(r.RequiredCRDs, func(gvr schema.GroupVersionResource) bool {
		return utils.CRDExists(r.Manager.GetClient().RESTMapper(), gvr)
	})
}

func (r *DynamicController) isOneOfRequiredCRDs(obj client.Object) bool {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return false
	}

	return lo.ContainsBy(r.RequiredCRDs, func(gvr schema.GroupVersionResource) bool {
		versionMatches := lo.ContainsBy(crd.Spec.Versions, func(crdv apiextensionsv1.CustomResourceDefinitionVersion) bool {
			return crdv.Name == gvr.Version
		})

		return crd.Spec.Group == gvr.Group &&
			crd.Status.AcceptedNames.Plural == gvr.Resource &&
			versionMatches
	})
}

func (r *DynamicController) setupControllers(mgr ctrl.Manager) error {
	errs := lo.FilterMap(r.Controllers, func(c Controller, _ int) (error, bool) {
		if err := c.SetupWithManager(mgr); err != nil {
			return err, true
		}
		return nil, false
	})

	return errors.Join(errs...)
}
