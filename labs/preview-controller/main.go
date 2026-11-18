package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var previewGVK = schema.GroupVersionKind{
	Group: "lab.example.com", Version: "v1alpha1", Kind: "ShippingPreview",
}

var errOwnershipConflict = errors.New("owned by another object or has no owner")

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctrl.SetLogger(zap.New())
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		return err
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	manager, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:  scheme,
		Cache:   cache.Options{DefaultNamespaces: map[string]cache.Config{"go-lab": {}}},
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return err
	}
	if err := ctrl.NewControllerManagedBy(manager).
		For(newPreview()).
		Owns(&appsv1.Deployment{}).
		Complete(&previewReconciler{Client: manager.GetClient(), scheme: scheme}); err != nil {
		return err
	}
	return manager.Start(ctrl.SetupSignalHandler())
}

func newPreview() *unstructured.Unstructured {
	preview := new(unstructured.Unstructured)
	preview.SetGroupVersionKind(previewGVK)
	return preview
}

type previewReconciler struct {
	client.Client
	scheme *runtime.Scheme
}

func (r *previewReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	preview := newPreview()
	if err := r.Get(ctx, request.NamespacedName, preview); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if preview.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, nil
	}

	replicas, found, err := unstructured.NestedInt64(preview.Object, "spec", "replicas")
	if err != nil || !found || replicas < 0 || replicas > 3 {
		return ctrl.Result{}, fmt.Errorf("invalid spec.replicas on %s", request.NamespacedName)
	}
	version, found, err := unstructured.NestedString(preview.Object, "spec", "version")
	if err != nil || !found || (version != "v1" && version != "v2") {
		return ctrl.Result{}, fmt.Errorf("invalid spec.version on %s", request.NamespacedName)
	}

	name := "preview-" + preview.GetName()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: request.Namespace}}
	desiredReplicas := int32(replicas)
	image := "go-shipping:" + version
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		if deployment.ResourceVersion == "" {
			initPreviewDeployment(deployment, preview.GetName())
			if err := controllerutil.SetControllerReference(preview, deployment, r.scheme,
				controllerutil.WithBlockOwnerDeletion(false)); err != nil {
				return err
			}
		} else if !metav1.IsControlledBy(deployment, preview) {
			return fmt.Errorf("deployment %s: %w", name, errOwnershipConflict)
		}

		deployment.Spec.Replicas = &desiredReplicas
		containers := &deployment.Spec.Template.Spec.Containers
		index := slices.IndexFunc(*containers, func(c corev1.Container) bool { return c.Name == "api" })
		if index == -1 {
			*containers = append(*containers, previewContainer(image))
		} else {
			(*containers)[index].Image = image
		}
		return nil
	})
	if err != nil {
		if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) || apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		condition := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Message: err.Error()}
		if errors.Is(err, errOwnershipConflict) {
			condition.Reason = "OwnershipConflict"
			// Unowned Deployments do not send events to this preview.
			return ctrl.Result{RequeueAfter: 5 * time.Second}, r.setStatus(ctx, preview, 0, condition)
		}
		condition.Reason = "ReconcileFailed"
		return ctrl.Result{}, errors.Join(err, r.setStatus(ctx, preview, 0, condition))
	}
	if result != controllerutil.OperationResultNone {
		ctrl.LoggerFrom(ctx).Info("Updated preview deployment", "deployment", name, "operation", result,
			"replicas", desiredReplicas, "image", image)
	}
	return ctrl.Result{}, r.setStatus(ctx, preview, deployment.Status.ReadyReplicas, readyCondition(deployment, desiredReplicas))
}

func initPreviewDeployment(deployment *appsv1.Deployment, previewName string) {
	labels := map[string]string{"lab.example.com/shipping-preview": previewName}
	deployment.Labels = labels
	deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	deployment.Spec.Template.Labels = labels
	automount := false
	nonRoot := true
	deployment.Spec.Template.Spec.AutomountServiceAccountToken = &automount
	deployment.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot:   &nonRoot,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func previewContainer(image string) corev1.Container {
	allowEscalation := false
	readOnly := true
	return corev1.Container{
		Name: "api", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromString("http")},
			},
			PeriodSeconds: 2,
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("25m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowEscalation,
			ReadOnlyRootFilesystem:   &readOnly,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}
