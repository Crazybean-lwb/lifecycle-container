/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	appv1 "lifecycle-container/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

// LiveContainerReconciler reconciles a LiveContainer object
type LiveContainerReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

///*
//We generally want to ignore (not requeue) NotFound errors, since we'll get a
//reconciliation request once the object exists, and requeuing in the meantime
//won't help.
//*/
//func ignoreNotFound(err error) error {
//	if apierrs.IsNotFound(err) {
//		return nil
//	}
//	return err
//}

//+kubebuilder:rbac:groups=app.kubebuilder.io,resources=livecontainers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=app.kubebuilder.io,resources=livecontainers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=app.kubebuilder.io,resources=livecontainers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the LiveContainer object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.1/pkg/reconcile
func (r *LiveContainerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger, _ := logr.FromContext(ctx)
	logger = logger.WithValues("livecontainer", req.NamespacedName)

	// TODO(user): your logic here
	// Fetch the LiveContainer instance
	instance := &appv1.LiveContainer{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// generate the Pod
	pod, err := generatePod(instance)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := ctrl.SetControllerReference(instance, pod, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := Mypod(ctx, r, pod, instance, logger); err != nil {
		return ctrl.Result{}, err
	}

	// Update the instance.Status.Phase if the foundPod.Status.Phase
	// has changed.
	foundPod := &corev1.Pod{}
	_err := r.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, foundPod)
	if _err != nil {
		if apierrs.IsNotFound(err) {
			logger.Info("Pod not found...", "pod", pod.Name)
		} else {
			return ctrl.Result{}, _err
		}
	}

	// Update status if needed
	if instance.Status.Phase != string(foundPod.Status.Phase) {
		logger.Info("Updating Status", "namespace", instance.Namespace, "name", instance.Name)
		if foundPod.Status.Reason == "DeadlineExceeded" {
			instance.Status.Phase = foundPod.Status.Reason
			logger.Info("Pod Timeout", "namespace", instance.Namespace, "name", instance.Name)

		} else {
			instance.Status.Phase = string(foundPod.Status.Phase)
			// update instance's node name before DeadlineExceeded
			instance.Status.AssignedNode = string(foundPod.Spec.NodeName)
		}

	}
	if err := r.Status().Update(ctx, instance); err != nil {
		return reconcile.Result{}, err
	}

	return ctrl.Result{}, nil
}

func Mypod(ctx context.Context, r *LiveContainerReconciler, pod *corev1.Pod, instance *appv1.LiveContainer, log logr.Logger) error {
	foundPod := &corev1.Pod{}
	justCreated := false
	if err := r.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, foundPod); err != nil {
		if apierrs.IsNotFound(err) {
			// Check if instance creation timestamp is older than the threshold
			createdTime := instance.CreationTimestamp.Unix()
			currentTime := time.Now().Unix()
			durationTime := currentTime - createdTime
			if durationTime >= instance.Spec.Timeout {
				return nil
			} else {
				log.Info("Creating Pod", "namespace", pod.Namespace, "name", pod.Name)
				if err := r.Create(ctx, pod); err != nil {
					log.Error(err, "unable to create pod")
					return err
				}
				justCreated = true
			}

		} else {
			log.Error(err, "error getting pod")
			return err
		}
	}
	if !justCreated && CopyPodSetFields(pod, foundPod) {
		log.Info("Updating Pod", "namespace", pod.Namespace, "name", pod.Name)
		if err := r.Update(ctx, foundPod); err != nil {
			log.Error(err, "unable to update pod")
			return err
		}
	}

	return nil
}

func CopyPodSetFields(from, to *corev1.Pod) bool {
	requireUpdate := false
	if from.Status.Phase != to.Status.Phase {
		to.Status.Phase = from.Status.Phase
		requireUpdate = true
	}

	return requireUpdate
}

func generatePod(instance *appv1.LiveContainer) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    map[string]string{"lifecycle-container-name": instance.Name},
			Annotations: map[string]string{
				"sidecar.istio.io/inject": "false",
			},
		},
		Spec: corev1.PodSpec{
			SchedulerName:         instance.Spec.SchedulerName,
			ActiveDeadlineSeconds: &instance.Spec.Timeout,
			Containers: []corev1.Container{
				{
					Name:            "virtual-box",
					Image:           instance.Spec.Image,
					ImagePullPolicy: corev1.PullPolicy(instance.Spec.ImagePullPolicy),
					Command:         []string{"/bin/bash", "-c"},
					Args:            []string{"cd /home && /etc/init.d/ssh start && while true; do sleep 30; done;"},
					Resources:       instance.Spec.Resources,
					Ports:           instance.Spec.Ports,
					VolumeMounts:    instance.Spec.VolumeMounts,
				},
			},
			Volumes: instance.Spec.Volumes,
		},
	}
	return pod, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LiveContainerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appv1.LiveContainer{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

