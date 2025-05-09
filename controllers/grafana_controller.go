/*
Copyright 2022.

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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/grafana/grafana-operator/v5/controllers/config"
	"github.com/grafana/grafana-operator/v5/controllers/metrics"
	"github.com/grafana/grafana-operator/v5/controllers/reconcilers"
	"github.com/grafana/grafana-operator/v5/controllers/reconcilers/grafana"
	v1 "k8s.io/api/apps/v1"
	v12 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	grafanav1beta1 "github.com/grafana/grafana-operator/v5/api/v1beta1"
	client2 "github.com/grafana/grafana-operator/v5/controllers/client"
)

// GrafanaReconciler reconciles a Grafana object
type GrafanaReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	IsOpenShift   bool
	ClusterDomain string
}

// +kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanas/finalizers,verbs=update
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes;routes/custom-host,verbs=get;list;create;update;delete;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;serviceaccounts;services;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

func (r *GrafanaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithName("GrafanaReconciler")
	ctx = logf.IntoContext(ctx, log)

	grafana := &grafanav1beta1.Grafana{}
	err := r.Get(ctx, req.NamespacedName, grafana)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("grafana cr has been deleted", "name", req.NamespacedName)
			return ctrl.Result{}, nil
		}

		log.Error(err, "error getting grafana cr")
		return ctrl.Result{}, err
	}

	metrics.GrafanaReconciles.WithLabelValues(grafana.Namespace, grafana.Name).Inc()

	defer func() {
		if err := r.Status().Update(ctx, grafana); err != nil {
			log.Error(err, "updating status")
		}
	}()

	if grafana.IsExternal() {
		grafana.Status.Stage = grafanav1beta1.OperatorStageComplete
		grafana.Status.AdminURL = grafana.Spec.External.URL
		version, err := r.getVersion(ctx, grafana)
		if err != nil {
			grafana.Status.Version = ""
			grafana.Status.LastMessage = err.Error()
			grafana.Status.StageStatus = grafanav1beta1.OperatorStageResultFailed

			return ctrl.Result{}, fmt.Errorf("failed to get version from external instance: %w", err)
		}

		grafana.Status.Version = version
		grafana.Status.LastMessage = ""
		grafana.Status.StageStatus = grafanav1beta1.OperatorStageResultSuccess
		return ctrl.Result{}, nil
	}

	// set spec to the current default version to avoid accidental updates when we
	// change the default. For clusters where RELATED_IMAGE_GRAFANA is set to an
	// image hash, we want to set this to the value of the variable to support air
	// gapped clusters as well
	if grafana.Spec.Version == "" {
		targetVersion := config.GrafanaVersion
		if envVersion := os.Getenv("RELATED_IMAGE_GRAFANA"); isImageSHA256(envVersion) {
			targetVersion = envVersion
		}

		grafana.Spec.Version = targetVersion
		if err := r.Update(ctx, grafana); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating grafana version in spec: %w", err)
		}
	}

	vars := &grafanav1beta1.OperatorReconcileVars{}
	for _, stage := range getInstallationStages() {
		log.Info("running stage", "stage", stage)

		grafana.Status.Stage = stage
		reconciler := r.getReconcilerForStage(stage)

		if reconciler == nil {
			log.Info("no reconciler known for stage", "stage", stage)
			continue
		}

		stageStatus, err := reconciler.Reconcile(ctx, grafana, vars, r.Scheme)
		if err != nil {
			grafana.Status.StageStatus = stageStatus // In progress or failed, both accompanied by Error
			grafana.Status.LastMessage = err.Error()
			metrics.GrafanaFailedReconciles.WithLabelValues(grafana.Namespace, grafana.Name, string(stage)).Inc()

			return ctrl.Result{}, fmt.Errorf("reconciler error in stage '%s': %w", stage, err)
		}
	}

	version, err := r.getVersion(ctx, grafana)
	if err != nil {
		grafana.Status.Version = ""
		grafana.Status.LastMessage = err.Error()
		grafana.Status.StageStatus = grafanav1beta1.OperatorStageResultFailed

		return ctrl.Result{}, fmt.Errorf("failed to get version from instance: %w", err)
	}

	grafana.Status.Version = version
	grafana.Status.StageStatus = grafanav1beta1.OperatorStageResultSuccess
	grafana.Status.LastMessage = ""
	return ctrl.Result{}, nil
}

func (r *GrafanaReconciler) getVersion(ctx context.Context, cr *grafanav1beta1.Grafana) (string, error) {
	cl, err := client2.NewHTTPClient(ctx, r.Client, cr)
	if err != nil {
		return "", fmt.Errorf("setup of the http client: %w", err)
	}

	instanceURL := cr.Status.AdminURL
	if instanceURL == "" && cr.Spec.External != nil {
		instanceURL = cr.Spec.External.URL
	}

	req, err := http.NewRequest("GET", instanceURL+"/api/frontend/settings", nil)
	if err != nil {
		return "", fmt.Errorf("building request to fetch version: %w", err)
	}

	err = client2.InjectAuthHeaders(context.Background(), r.Client, cr, req)
	if err != nil {
		return "", fmt.Errorf("fetching authentication information for version detection: %w", err)
	}

	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching version: %w", err)
	}

	data := struct {
		BuildInfo struct {
			Version string `json:"version"`
		} `json:"buildInfo"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("parsing health endpoint data: %w", err)
	}
	if data.BuildInfo.Version == "" {
		return "", fmt.Errorf("empty version received from server")
	}

	return data.BuildInfo.Version, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GrafanaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&grafanav1beta1.Grafana{}).
		Owns(&v1.Deployment{}).
		Owns(&v12.ConfigMap{}).
		WithOptions(controller.Options{RateLimiter: defaultRateLimiter()}).
		Complete(r)
}

func getInstallationStages() []grafanav1beta1.OperatorStageName {
	return []grafanav1beta1.OperatorStageName{
		grafanav1beta1.OperatorStageAdminUser,
		grafanav1beta1.OperatorStageGrafanaConfig,
		grafanav1beta1.OperatorStagePvc,
		grafanav1beta1.OperatorStageServiceAccount,
		grafanav1beta1.OperatorStageService,
		grafanav1beta1.OperatorStageIngress,
		grafanav1beta1.OperatorStagePlugins,
		grafanav1beta1.OperatorStageDeployment,
		grafanav1beta1.OperatorStageComplete,
	}
}

func (r *GrafanaReconciler) getReconcilerForStage(stage grafanav1beta1.OperatorStageName) reconcilers.OperatorGrafanaReconciler {
	switch stage {
	case grafanav1beta1.OperatorStageGrafanaConfig:
		return grafana.NewConfigReconciler(r.Client)
	case grafanav1beta1.OperatorStageAdminUser:
		return grafana.NewAdminSecretReconciler(r.Client)
	case grafanav1beta1.OperatorStagePvc:
		return grafana.NewPvcReconciler(r.Client)
	case grafanav1beta1.OperatorStageServiceAccount:
		return grafana.NewServiceAccountReconciler(r.Client)
	case grafanav1beta1.OperatorStageService:
		return grafana.NewServiceReconciler(r.Client, r.ClusterDomain)
	case grafanav1beta1.OperatorStageIngress:
		return grafana.NewIngressReconciler(r.Client, r.IsOpenShift)
	case grafanav1beta1.OperatorStagePlugins:
		return grafana.NewPluginsReconciler(r.Client)
	case grafanav1beta1.OperatorStageDeployment:
		return grafana.NewDeploymentReconciler(r.Client, r.IsOpenShift)
	case grafanav1beta1.OperatorStageComplete:
		return grafana.NewCompleteReconciler()
	default:
		return nil
	}
}

func isImageSHA256(image string) bool {
	return strings.Contains(image, "@sha256:")
}
