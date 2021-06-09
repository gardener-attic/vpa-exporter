// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	autoscaling "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1beta2"
	clientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	informerfactory "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/informers/externalversions"
	informers "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/informers/externalversions/autoscaling.k8s.io/v1beta2"
	listers "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1beta2"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

const (
	labelName                               = "name"
	labelNamespace                          = "namespace"
	labelContainer                          = "container"
	labelUpdatePolicy                       = "updatePolicy"
	labelContainerResourcePolicyMode        = "containerResourcePolicyMode"
	labelResource                           = "resource"
	labelAllowed                            = "allowed"
	labelRecommendation                     = "recommendation"
	labelConditionType                      = "conditionType"
	labelConditionReason                    = "conditionReason"
	minAllowed                              = "min"
	maxAllowed                              = "max"
	targetRecommendation                    = "target"
	lowerBoundRecommendation                = "lowerBound"
	upperBoundRecommendation                = "upperBound"
	uncappedTargetRecommendation            = "uncappedTarget"
	vpaNamespace                            = "vpa"
	gardener_vpa                            = "gardener_vpa"
	gardenerVPARecommendationAnnnotationKey = "vpa-recommender.gardener.cloud/status"
	subsystemMetadata                       = "metadata"
	subsystemSpec                           = "spec"
	subsystemStatus                         = "status"
	labelTargetRefName                      = "targetName"
	labelTargetRefKind                      = "targetKind"
)

var (
	masterURL  string
	kubeconfig string
	namespace  string
	port       int

	defaultSyncDuration = time.Second * 30

	descVerticalPodAutoscalerLabelsName          = "kube_vpa_labels"
	descVerticalPodAutoscalerLabelsHelp          = "Kubernetes labels converted to Prometheus labels."
	descVerticalPodAutoscalerLabelsDefaultLabels = []string{"namespace", "vpa"}

	vpaMetadataGeneration = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: vpaNamespace,
			Subsystem: subsystemMetadata,
			Name:      "generation",
			Help:      "The generation observed by the VerticalPodAutoscaler controller.",
		},
		[]string{labelName, labelNamespace},
	)

	vpaSpecContainerResourcePolicyAllowed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: vpaNamespace,
			Subsystem: subsystemSpec,
			Name:      "container_resource_policy_allowed",
			Help:      "The container resource allowed mentioned in the resouce policy in the VerticalPodAutoscaler spec.",
		},
		[]string{labelName, labelNamespace, labelContainer, labelAllowed, labelResource, labelUpdatePolicy, labelTargetRefName, labelTargetRefKind},
	)

	vpaStatusRecommendation = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: vpaNamespace,
			Subsystem: subsystemStatus,
			Name:      "recommendation",
			Help:      "The resource recommendation for a container in the VerticalPodAutoscaler status.",
		},
		[]string{labelName, labelNamespace, labelContainer, labelRecommendation, labelResource, labelUpdatePolicy, labelTargetRefName, labelTargetRefKind},
	)

	vpaGardenerRecommendation = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: gardener_vpa,
			Subsystem: subsystemStatus,
			Name:      "recommendation",
			Help:      "The resource recommendation for a container by the gardener/vpa-recommender.",
		},
		[]string{labelName, labelNamespace, labelContainer, labelRecommendation, labelResource, labelUpdatePolicy, labelTargetRefName, labelTargetRefKind},
	)
)

func init() {
	prometheus.MustRegister(vpaMetadataGeneration)
	prometheus.MustRegister(vpaSpecContainerResourcePolicyAllowed)
	prometheus.MustRegister(vpaStatusRecommendation)
	prometheus.MustRegister(vpaGardenerRecommendation)
}

// Controller listens to add, update and deletion of VPA resources
// and updates the Prometheus metrices accordingly.
type Controller struct {
	clientset clientset.Interface
	lister    listers.VerticalPodAutoscalerLister
	synced    cache.InformerSynced
	workqueue workqueue.RateLimitingInterface
}

// NewController creates an instance of the Controller resource to listen to changes in
// VPA resources. The changes are then fed to the Prometheus endpoint for scraping.
func NewController(
	clientset clientset.Interface,
	informer informers.VerticalPodAutoscalerInformer) *Controller {

	klog.V(4).Info("Creating event broadcaster")

	controller := &Controller{
		clientset: clientset,
		lister:    informer.Lister(),
		synced:    informer.Informer().HasSynced,
		workqueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "VPAs"),
	}

	klog.Info("Setting up event handlers")
	informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueVPA,
		UpdateFunc: func(old, new interface{}) {
			newDepl := new.(*autoscaling.VerticalPodAutoscaler)
			oldDepl := old.(*autoscaling.VerticalPodAutoscaler)
			if newDepl.ResourceVersion == oldDepl.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployment will always have different RVs.
				return
			}
			controller.enqueueVPA(new)
		},
		DeleteFunc: controller.enqueueVPA,
	})

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting vpa-exporter controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.synced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch workers to process VPA resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// VPA resource to be synced.
		if err := c.syncHandler(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the VPA resource
// with the current status of the resource.
func (c *Controller) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the VPA resource with this namespace/name
	vpa, err := c.lister.VerticalPodAutoscalers(namespace).Get(name)
	if err != nil {
		// The VPA resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("vpa '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	// Finally, we update the VPA metrics
	err = c.updateVPAMetrics(vpa)
	if err != nil {
		return err
	}

	return nil
}

// enqueueVPA takes a VPA resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than VPA.
func (c *Controller) enqueueVPA(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.AddRateLimited(key)
}

func addReco(containerName, recommendation, resource string, q resource.Quantity, metric *prometheus.GaugeVec, vpa *autoscaling.VerticalPodAutoscaler) {
	labels := prometheus.Labels{
		labelNamespace:      vpa.ObjectMeta.Namespace,
		labelName:           vpa.ObjectMeta.Name,
		labelContainer:      containerName,
		labelRecommendation: recommendation,
		labelResource:       resource,
		labelTargetRefName:  vpa.Spec.TargetRef.Name,
		labelTargetRefKind:  vpa.Spec.TargetRef.Kind,
	}
	if vpa.Spec.UpdatePolicy != nil && vpa.Spec.UpdatePolicy.UpdateMode != nil {
		labels[labelUpdatePolicy] = string(*vpa.Spec.UpdatePolicy.UpdateMode)
	}

	// CPU metrics must be exposed as millicores
	if resource == "cpu" {
		metric.With(labels).Set(float64(q.MilliValue()))
	} else {
		metric.With(labels).Set(float64(q.Value()))
	}
}

func (c *Controller) updateVPAMetrics(vpa *autoscaling.VerticalPodAutoscaler) error {
	vpaMetadataGeneration.With(prometheus.Labels{
		labelNamespace: vpa.ObjectMeta.Namespace,
		labelName:      vpa.ObjectMeta.Name,
	}).Set(float64(vpa.ObjectMeta.Generation))

	if vpa.Spec.TargetRef == nil {
		klog.Warningf("Skipping VerticalPodAutoscaler without targetRef %s/%s.", vpa.Namespace, vpa.Name)
		return nil
	}

	if vpa.Spec.ResourcePolicy != nil {
		addAllowed := func(containerName, allowed, resource string, q resource.Quantity) {
			labels := prometheus.Labels{
				labelNamespace:     vpa.ObjectMeta.Namespace,
				labelName:          vpa.ObjectMeta.Name,
				labelContainer:     containerName,
				labelAllowed:       allowed,
				labelResource:      resource,
				labelTargetRefName: vpa.Spec.TargetRef.Name,
				labelTargetRefKind: vpa.Spec.TargetRef.Kind,
			}
			if vpa.Spec.UpdatePolicy != nil && vpa.Spec.UpdatePolicy.UpdateMode != nil {
				labels[labelUpdatePolicy] = string(*vpa.Spec.UpdatePolicy.UpdateMode)
			}

			vpaSpecContainerResourcePolicyAllowed.With(labels).Set(float64(q.Value()))
		}

		for _, crp := range vpa.Spec.ResourcePolicy.ContainerPolicies {
			for k, q := range crp.MinAllowed {
				addAllowed(crp.ContainerName, minAllowed, string(k), q)
			}
			for k, q := range crp.MaxAllowed {
				addAllowed(crp.ContainerName, maxAllowed, string(k), q)
			}
		}
	}

	if vpa.Status.Recommendation != nil {

		for _, cr := range vpa.Status.Recommendation.ContainerRecommendations {
			for k, q := range cr.Target {
				addReco(cr.ContainerName, targetRecommendation, string(k), q, vpaStatusRecommendation, vpa)
			}
			for k, q := range cr.LowerBound {
				addReco(cr.ContainerName, lowerBoundRecommendation, string(k), q, vpaStatusRecommendation, vpa)
			}
			for k, q := range cr.UpperBound {
				addReco(cr.ContainerName, upperBoundRecommendation, string(k), q, vpaStatusRecommendation, vpa)
			}
			for k, q := range cr.UncappedTarget {
				addReco(cr.ContainerName, uncappedTargetRecommendation, string(k), q, vpaStatusRecommendation, vpa)
			}
		}
	}

	gardenerVPAStatusJSON, hasGardenerVPAStatusJSON := vpa.ObjectMeta.Annotations[gardenerVPARecommendationAnnnotationKey]

	if hasGardenerVPAStatusJSON && gardenerVPAStatusJSON != "" {

		gardenerVPAStatus := &autoscaling.VerticalPodAutoscalerStatus{}
		err := json.Unmarshal([]byte(gardenerVPAStatusJSON), gardenerVPAStatus)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("error in unmarshalling gardenerVPAStatus"))
			return nil
		}

		for _, cr := range gardenerVPAStatus.Recommendation.ContainerRecommendations {
			for k, q := range cr.Target {
				addReco(cr.ContainerName, targetRecommendation, string(k), q, vpaGardenerRecommendation, vpa)
			}
			for k, q := range cr.LowerBound {
				addReco(cr.ContainerName, lowerBoundRecommendation, string(k), q, vpaGardenerRecommendation, vpa)
			}
			for k, q := range cr.UpperBound {
				addReco(cr.ContainerName, upperBoundRecommendation, string(k), q, vpaGardenerRecommendation, vpa)
			}
			for k, q := range cr.UncappedTarget {
				addReco(cr.ContainerName, uncappedTargetRecommendation, string(k), q, vpaGardenerRecommendation, vpa)
			}
		}
	}

	return nil
}

var onlyOneSignalHandler = make(chan struct{})

var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// setupSignalHandler registered for SIGTERM and SIGINT. A stop channel is returned
// which is closed on one of these signals. If a second signal is caught, the program
// is terminated with exit code 1.
func setupSignalHandler() (stopCh <-chan struct{}) {
	close(onlyOneSignalHandler) // panics when called twice

	stop := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, shutdownSignals...)
	go func() {
		<-c
		close(stop)
		<-c
		os.Exit(1) // second signal. Exit directly.
	}()

	return stop
}

func main() {
	flag.Parse()
	var informerFactory informerfactory.SharedInformerFactory
	// set up signals so we handle the first shutdown signal gracefully
	stopCh := setupSignalHandler()

	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if err != nil {
		klog.Fatalf("Error building kubeconfig: %s", err.Error())
	}

	client, err := clientset.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building example clientset: %s", err.Error())
	}

	if len(namespace) == 0 {
		informerFactory = informerfactory.NewSharedInformerFactory(client, defaultSyncDuration)
	} else {
		informerFactory = informerfactory.NewFilteredSharedInformerFactory(client, defaultSyncDuration, namespace, nil)
	}

	controller := NewController(client, informerFactory.Autoscaling().V1beta2().VerticalPodAutoscalers())

	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(stopCh)
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	informerFactory.Start(stopCh)

	go serveMetrics()

	if err = controller.Run(1, stopCh); err != nil {
		klog.Fatalf("Error running controller: %s", err.Error())
	}
}

func serveMetrics() error {
	http.Handle("/metrics", promhttp.Handler())
	return http.ListenAndServe(fmt.Sprintf("%s%d", ":", port), nil)
}

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&namespace, "namespace", "", "Namespace in which the VPA resources have to be listened to.")
	flag.IntVar(&port, "port", 9570, "The port on which prometheus metrics are exposed.")
}
