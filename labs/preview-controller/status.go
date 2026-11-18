package main

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type previewStatus struct {
	ReadyReplicas int32              `json:"readyReplicas"`
	Conditions    []metav1.Condition `json:"conditions"`
}

func readyCondition(deployment *appsv1.Deployment, desired int32) metav1.Condition {
	condition := metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "DeploymentProgressing",
		Message: fmt.Sprintf("Waiting for deployment %s to make %d replicas available", deployment.Name, desired),
	}
	status := deployment.Status
	if status.ObservedGeneration != deployment.Generation {
		return condition
	}
	if status.UpdatedReplicas == desired && status.AvailableReplicas == desired && status.Replicas == desired {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "DeploymentAvailable"
		condition.Message = fmt.Sprintf("Deployment %s has %d updated and available replicas", deployment.Name, desired)
		return condition
	}
	for _, progress := range status.Conditions {
		if progress.Type == appsv1.DeploymentProgressing && progress.Status == corev1.ConditionFalse {
			condition.Reason = "DeploymentRolloutFailed"
			condition.Message = progress.Message
			break
		}
	}
	return condition
}

func (r *previewReconciler) setStatus(ctx context.Context, preview *unstructured.Unstructured,
	readyReplicas int32, condition metav1.Condition,
) error {
	current, _, err := unstructured.NestedMap(preview.Object, "status")
	if err != nil {
		return err
	}
	var status previewStatus
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(current, &status); err != nil {
		return err
	}
	condition.ObservedGeneration = preview.GetGeneration()
	changed := meta.SetStatusCondition(&status.Conditions, condition)
	if !changed && status.ReadyReplicas == readyReplicas {
		return nil
	}
	status.ReadyReplicas = readyReplicas

	next, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&status)
	if err != nil {
		return err
	}
	before := preview.DeepCopy()
	if err := unstructured.SetNestedMap(preview.Object, next, "status"); err != nil {
		return err
	}
	return r.Status().Patch(ctx, preview, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}
