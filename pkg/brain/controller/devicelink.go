package controller

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/rancher/octopus/api/v1alpha1"
	"github.com/rancher/octopus/pkg/brain/predicate"
	limbctrl "github.com/rancher/octopus/pkg/limb/controller"
	"github.com/rancher/octopus/pkg/util/collection"
	modelutil "github.com/rancher/octopus/pkg/util/model"
	"github.com/rancher/octopus/pkg/util/object"
)

// DeviceLinkReconciler reconciles a DeviceLink object
type DeviceLinkReconciler struct {
	client.Client

	Ctx context.Context
	Log logr.Logger
}

// +kubebuilder:rbac:groups=edge.cattle.io,resources=devicelinks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=edge.cattle.io,resources=devicelinks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get
// +kubebuilder:rbac:groups="apiextensions.k8s.io",resources=customresourcedefinitions,verbs=get

func (r *DeviceLinkReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	// 主要就是确认node的状态和model 的状态是否存在
	var ctx = r.Ctx
	var log = r.Log.WithValues("deviceLink", req.NamespacedName)

	// fetches link
	var link edgev1alpha1.DeviceLink
	//  j先检测link 资源存在不存在
	if err := r.Get(ctx, req.NamespacedName, &link); err != nil {
		if !apierrs.IsNotFound(err) {
			log.Error(err, "Unable to fetch DeviceLink")
			return ctrl.Result{Requeue: true}, nil
		}
		// ignores error, since they can't be fixed by an immediate requeue
		return ctrl.Result{}, nil
	}

	if object.IsDeleted(&link) {
		// NB(thxCode) the limb's finalizer needs to be removed if the Node is deleted(the limb is deleted too).
		if !collection.StringSliceContain(link.Finalizers, limbctrl.ReconcilingDeviceLink) {
			return ctrl.Result{}, nil
		}

		var isControlledByLimb bool
		if link.GetNodeExistedStatus() != metav1.ConditionFalse {
			var node corev1.Node
			if err := r.Get(ctx, types.NamespacedName{Name: link.Spec.Adaptor.Node}, &node); err != nil {
				if !apierrs.IsNotFound(err) {
					log.Error(err, "Unable to fetch the adaptor node of DeviceLink")
					return ctrl.Result{Requeue: true}, nil
				}
			}
			isControlledByLimb = object.IsActivating(&node)
		}
		if !isControlledByLimb {
			link.Finalizers = collection.StringSliceRemove(link.Finalizers, limbctrl.ReconcilingDeviceLink)
			if err := r.Update(ctx, &link); err != nil {
				log.Error(err, "Unable to remove finalizer from DeviceLink")
				return ctrl.Result{Requeue: true}, nil
			}
		}
		return ctrl.Result{}, nil
	}

	// verifies Node
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: link.Spec.Adaptor.Node}, &node); err != nil {
		// 如果设备没有找到，重新requeue ，在进行reconcile
		if !apierrs.IsNotFound(err) {
			log.Error(err, "Unable to fetch the adaptor node of DeviceLink")
			return ctrl.Result{Requeue: true}, nil
		}
	}
	if !object.IsActivating(&node) {
		// 检查node是否处于活动状态， 不在活动状态，link 资源重新requeue
		link.FailOnNodeExisted("adaptor node isn't existed")
		if err := r.Status().Update(ctx, &link); err != nil {
			log.Error(err, "Unable to change the status of DeviceLink")
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, nil
	}
	link.SucceedOnNodeExisted(&node)
	//设置节点的状态为existed

	// verifies CRD， 主要校验yaml文件是否合法
	var model = apiextensionsv1.CustomResourceDefinition{}
	if err := r.Get(ctx, types.NamespacedName{Name: modelutil.GetCRDNameOfGroupVersionKind(link.Spec.Model.GroupVersionKind())}, &model); err != nil {
		if !apierrs.IsNotFound(err) {
			log.Error(err, "Unable to fetch the model of DeviceLink")
			return ctrl.Result{Requeue: true}, nil
		}
	}
	if !object.IsActivating(&model) {
		link.FailOnModelExisted("model isn't existed")
		if err := r.Status().Update(ctx, &link); err != nil {
			log.Error(err, "Unable to change the status of DeviceLink")
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, nil
	}
	if !isModelAccepted(&link, &model) {
		link.FailOnModelExisted("model version isn't served")
		if err := r.Status().Update(ctx, &link); err != nil {
			log.Error(err, "Unable to change the status of DeviceLink")
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, nil
	}
	link.SucceedOnModelExisted()

	if err := r.Status().Update(ctx, &link); err != nil {
		log.Error(err, "Unable to change the status of DeviceLink")
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DeviceLinkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("brain_dl").
		For(&edgev1alpha1.DeviceLink{}).
		WithEventFilter(predicate.DeviceLinkChangedPredicate{}).
		Complete(r)
}
