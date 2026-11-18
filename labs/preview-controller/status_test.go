package main

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReadyCondition(t *testing.T) {
	for _, tc := range []struct {
		name       string
		desired    int32
		status     appsv1.DeploymentStatus
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "previous generation was available",
			desired:    2,
			status:     appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2},
			wantStatus: metav1.ConditionFalse,
			wantReason: "DeploymentProgressing",
		},
		{
			name:       "available replicas belong to the old revision",
			desired:    2,
			status:     appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 2},
			wantStatus: metav1.ConditionFalse,
			wantReason: "DeploymentProgressing",
		},
		{
			name:       "all desired replicas are available",
			desired:    2,
			status:     appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2},
			wantStatus: metav1.ConditionTrue,
			wantReason: "DeploymentAvailable",
		},
		{
			name:       "intentionally scaled to zero",
			status:     appsv1.DeploymentStatus{ObservedGeneration: 2},
			wantStatus: metav1.ConditionTrue,
			wantReason: "DeploymentAvailable",
		},
		{
			name:    "deployment reported a failed rollout",
			desired: 2,
			status: appsv1.DeploymentStatus{
				ObservedGeneration: 2,
				Conditions: []appsv1.DeploymentCondition{{
					Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
					Reason: "ProgressDeadlineExceeded", Message: "Rollout timed out",
				}},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "DeploymentRolloutFailed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "preview", Generation: 2},
				Status:     tc.status,
			}
			condition := readyCondition(deployment, tc.desired)
			if condition.Status != tc.wantStatus || condition.Reason != tc.wantReason {
				t.Fatalf("Ready condition = %s/%s, want %s/%s", condition.Status, condition.Reason, tc.wantStatus, tc.wantReason)
			}
		})
	}
}
