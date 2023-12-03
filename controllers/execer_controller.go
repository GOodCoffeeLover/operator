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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	executorsv1alpha1 "github.com/goodcoffeelover/operator/api/v1alpha1"
)

var (
	controlledByLabel  = fmt.Sprintf("%v/owner-uid", executorsv1alpha1.GroupVersion.Group)
	podControllerField = ".metadata.controller.uid"
)

// ExecerReconciler reconciles a Execer object
type ExecerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=executors.schizo.io,resources=execers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=executors.schizo.io,resources=execers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=executors.schizo.io,resources=execers/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=pods,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Execer object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *ExecerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	execer := &executorsv1alpha1.Execer{}

	if err := r.Get(ctx, req.NamespacedName, execer); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Execer resource not found. Ignoring since object must be deleted")
			return reconcile.Result{}, nil
		}
		log.Error(err, "Failed to get Execer")
		return reconcile.Result{}, err
	}
	log.Info("Got Execer", "Name", execer.Name, "UUID", execer.UID)

	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(execer.Namespace),
		client.MatchingFields{podControllerField: string(execer.UID)},
	); err != nil {
		log.Error(err, "Failed to list owned pods")
		return reconcile.Result{}, err
	}

	log.Info(fmt.Sprintf("Got %d pods in %s, continue: %v", len(podList.Items), execer.Namespace, podList.Continue))

	if len(podList.Items) == 0 {
		if execer.Status.Status == "Finished" {
			log.Info("Seems pod terminating")
			return ctrl.Result{}, nil
		}
		if err := r.createPod(ctx, execer); err != nil {
			log.Error(err, "Failed to create pod")
			return reconcile.Result{}, err
		}
		execer.Status.Status = "Pending"
		if err := r.Status().Update(ctx, execer); err != nil {
			log.Error(err, "Failed to update execer")
			return reconcile.Result{}, err
		}
		log.Info("Pod created")
		return ctrl.Result{}, nil
	}

	pod := podList.Items[0]
	log.Info(fmt.Sprintf("%v has status %v", pod.Name, pod.Status.Phase))
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		if err := r.Delete(ctx, &pod, client.PropagationPolicy(v1.DeletePropagationBackground)); err != nil {
			log.Error(err, "Failed to delete pod %v/%v", pod.Namespace, pod.Name)
			return reconcile.Result{}, err
		}
		execer.Status.Status = "Finished"
		if err := r.Status().Update(ctx, execer); err != nil {
			log.Error(err, "Failed to update execer")
			return reconcile.Result{}, err
		}
		log.Info("Execer finished")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *ExecerReconciler) createPod(ctx context.Context, execer *executorsv1alpha1.Execer) error {
	pod := &corev1.Pod{
		ObjectMeta: v1.ObjectMeta{
			GenerateName: fmt.Sprintf("%v-", execer.Name),
			Namespace:    execer.Namespace,
			Labels: map[string]string{
				controlledByLabel: string(execer.UID),
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "main",
					Image:   "ubuntu",
					Command: []string{"sh", "-c", "echo \"executed\""},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	ctrl.SetControllerReference(execer, pod, r.Scheme)
	return r.Create(ctx, pod)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ExecerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, podControllerField,
		func(rawObj client.Object) []string {
			// grab the job object, extract the owner...
			pod := rawObj.(*corev1.Pod)
			owner := v1.GetControllerOf(pod)
			if owner == nil {
				return nil
			}
			// ...make sure it's a CronJob...
			if owner.APIVersion != executorsv1alpha1.GroupVersion.String() || owner.Kind != "Execer" {
				return nil
			}
			// ...and if so, return it
			return []string{string(owner.UID)}
		}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&executorsv1alpha1.Execer{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
