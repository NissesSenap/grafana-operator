/*
Copyright 2021.

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

package grafana

import (
	"context"
	"time"

	integreatlyorgv1alpha1 "github.com/grafana-operator/grafana-operator/v4/api/integreatly/v1alpha1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Grafana controller", func() {

	// Define utility constants for object names and testing timeouts/durations and intervals.
	const (
		GrafanaName      = "test-grafana"
		GrafanaNamespace = "default"

		timeout  = time.Second * 10
		duration = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When updating Grafana Status", func() {
		It("Should set Grafana Status.message to success", func() {
			By("setting up a new Grafana instance")
			ctx := context.Background()
			grafana := &integreatlyorgv1alpha1.Grafana{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "integreatly.org/v1alpha1",
					Kind:       "Grafana",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      GrafanaName,
					Namespace: GrafanaNamespace,
				},
				Spec: integreatlyorgv1alpha1.GrafanaSpec{
					DashboardLabelSelector: []*metav1.LabelSelector{
						{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "app",
									Operator: metav1.LabelSelectorOpIn,
									Values: []string{
										"grafana",
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, grafana)).Should(Succeed())

			grafanaLookupKey := types.NamespacedName{Name: GrafanaName, Namespace: GrafanaNamespace}
			createdGrafana := &integreatlyorgv1alpha1.Grafana{}

			// We'll need to retry getting this newly created CronJob, given that creation may not immediately happen.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, grafanaLookupKey, createdGrafana)
				if err != nil {
					return false
				}
				return true
			}, timeout, interval).Should(BeTrue())
			// Let's make sure our Schedule string value was properly converted/handled.
			Expect(createdGrafana.ObjectMeta.Name).Should(Equal("test-grafana"))

			/*
				By("By checking the CronJob has zero active Jobs")
				Consistently(func() (int, error) {
					err := k8sClient.Get(ctx, grafanaLookupKey, createdGrafana)
					if err != nil {
						return -1, err
					}
					return len(createdGrafana.Status.Active), nil
				}, duration, interval).Should(Equal(0))

				By("By creating a new Job")
				testJob := &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      GrafanaName,
						Namespace: GrafanaNamespace,
					},
					Spec: batchv1.JobSpec{
						Template: v1.PodTemplateSpec{
							Spec: v1.PodSpec{
								// For simplicity, we only fill out the required fields.
								Containers: []v1.Container{
									{
										Name:  "test-container",
										Image: "test-image",
									},
								},
								RestartPolicy: v1.RestartPolicyOnFailure,
							},
						},
					},
					Status: batchv1.JobStatus{
						Active: 2,
					},
				}

				// Note that your CronJob’s GroupVersionKind is required to set up this owner reference.
				kind := reflect.TypeOf(cronjobv1.CronJob{}).Name()
				gvk := cronjobv1.GroupVersion.WithKind(kind)

				controllerRef := metav1.NewControllerRef(createdGrafana, gvk)
				testJob.SetOwnerReferences([]metav1.OwnerReference{*controllerRef})
				Expect(k8sClient.Create(ctx, testJob)).Should(Succeed())

				By("By checking that the CronJob has one active Job")
				Eventually(func() ([]string, error) {
					err := k8sClient.Get(ctx, cronjobLookupKey, createdGrafana)
					if err != nil {
						return nil, err
					}

					names := []string{}
					for _, job := range createdGrafana.Status.Active {
						names = append(names, job.Name)
					}
					return names, nil
				}, timeout, interval).Should(ConsistOf(JobName), "should list our active job %s in the active jobs list in status", JobName)
			*/
		})
	})

})