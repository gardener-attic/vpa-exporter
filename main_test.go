package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	autoscaling "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	vpa "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1beta2"
	vpaclient "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	fakevpaclient "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/fake"
	informerfactory "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/informers/externalversions"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

type fixture struct {
	t               *testing.T
	client          vpaclient.Interface
	informerFactory informerfactory.SharedInformerFactory
	vpasSynced      cache.InformerSynced
	controller      *Controller
	watcher         *watch.FakeWatcher
	stopCh          chan struct{}
}

func toRuntimeObjects(vpas ...*vpa.VerticalPodAutoscaler) (ro []runtime.Object) {
	ro = make([]runtime.Object, len(vpas))
	for i := range vpas {
		ro[i] = vpas[i]
	}
	return
}
func newFixture(t *testing.T, vpas ...*vpa.VerticalPodAutoscaler) (*fixture, error) {
	f := &fixture{
		t:       t,
		watcher: watch.NewFakeWithChanSize(1, false),
		stopCh:  make(chan struct{}),
	}

	{
		client := fakevpaclient.NewSimpleClientset(toRuntimeObjects(vpas...)...)
		client.PrependWatchReactor("verticalpodautoscalers", k8stesting.DefaultWatchReactor(f.watcher, nil))
		f.client = client
	}
	f.informerFactory = informerfactory.NewSharedInformerFactory(f.client, defaultSyncDuration)
	{
		informer := f.informerFactory.Autoscaling().V1beta2().VerticalPodAutoscalers()
		f.vpasSynced = informer.Informer().HasSynced
		f.controller = NewController(f.client, informer)
	}

	return f, nil
}

func (f *fixture) run() {
	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(stopCh)
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	f.informerFactory.Start(f.stopCh)

	f.controller.Run(1, f.stopCh)
}

func (f *fixture) stop() {
	defer close(f.stopCh)
	f.watcher.Stop()

}

func podUpdatePolicy(updateMode vpa.UpdateMode) *vpa.PodUpdatePolicy {
	return &vpa.PodUpdatePolicy{UpdateMode: &updateMode}
}

func containerScalingMode(scalingMode vpa.ContainerScalingMode) *vpa.ContainerScalingMode {
	return &scalingMode
}

func getResources(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}
}

func resetMetrics(gatherer bool) {
	vpaMetadataGeneration.Reset()
	vpaSpecContainerResourcePolicyAllowed.Reset()
	vpaStatusRecommendation.Reset()
	vpaGardenerRecommendation.Reset()

	if gatherer {
		mfs, _ := prometheus.DefaultGatherer.Gather()
		for _, mf := range mfs {
			for _, mfm := range mf.GetMetric() {
				mfm.Reset()
			}
		}
	}
}

func stringPtr(s string) *string {
	return &s
}

func metricType(mt dto.MetricType) *dto.MetricType {
	return &mt
}

func float64Ptr(f float64) *float64 {
	return &f
}

func metadataGenerationMetricFamily(vs ...*vpa.VerticalPodAutoscaler) *dto.MetricFamily {
	if len(vs) < 1 {
		return nil
	}

	mf := &dto.MetricFamily{
		Name: stringPtr(prometheus.BuildFQName(vpaNamespace, subsystemMetadata, "generation")),
		Type: metricType(dto.MetricType_GAUGE),
	}

	for _, v := range vs {
		mf.Metric = append(mf.Metric, &dto.Metric{
			Label: []*dto.LabelPair{
				&dto.LabelPair{
					Name:  stringPtr(labelName),
					Value: stringPtr(v.Name),
				},
				&dto.LabelPair{
					Name:  stringPtr(labelNamespace),
					Value: stringPtr(v.Namespace),
				},
			},
			Gauge: &dto.Gauge{
				Value: float64Ptr(float64(v.Generation)),
			},
		})
	}
	return mf
}

func specContainerResourcePolicyAllowedMetricFamily(vs ...*vpa.VerticalPodAutoscaler) (valid *dto.MetricFamily, invalid *dto.MetricFamily) {
	if len(vs) < 1 {
		return
	}

	valid = &dto.MetricFamily{
		Name: stringPtr(prometheus.BuildFQName(vpaNamespace, subsystemSpec, "container_resource_policy_allowed")),
		Type: metricType(dto.MetricType_GAUGE),
	}
	invalid = &dto.MetricFamily{
		Name: stringPtr(prometheus.BuildFQName(vpaNamespace, subsystemSpec, "container_resource_policy_allowed")),
		Type: metricType(dto.MetricType_GAUGE),
	}

	for _, v := range vs {
		var (
			targetRefKind string
			targetRefName string
			isInvalid     bool
		)

		if v.Spec.TargetRef == nil {
			isInvalid = true
		} else {
			targetRefKind = v.Spec.TargetRef.Kind
			targetRefName = v.Spec.TargetRef.Name
		}

		lpName := &dto.LabelPair{
			Name:  stringPtr(labelName),
			Value: stringPtr(v.Name),
		}
		lpNamespace := &dto.LabelPair{
			Name:  stringPtr(labelNamespace),
			Value: stringPtr(v.Namespace),
		}
		lpTargetRefKind := &dto.LabelPair{
			Name:  stringPtr(labelTargetRefKind),
			Value: stringPtr(targetRefKind),
		}
		lpTargetRefName := &dto.LabelPair{
			Name:  stringPtr(labelTargetRefName),
			Value: stringPtr(targetRefName),
		}

		if v.Spec.ResourcePolicy == nil || v.Spec.ResourcePolicy.ContainerPolicies == nil {
			isInvalid = true
			m := &dto.Metric{
				Label: []*dto.LabelPair{
					lpName,
					lpNamespace,
					lpTargetRefKind,
					lpTargetRefName,
				},
			}
			invalid.Metric = append(invalid.Metric, m)

			continue
		}

		var mfPtr **dto.MetricFamily
		if isInvalid {
			mfPtr = &invalid
		} else {
			mfPtr = &valid
		}

		for i := range v.Spec.ResourcePolicy.ContainerPolicies {
			cp := &v.Spec.ResourcePolicy.ContainerPolicies[i]
			lpContainer := &dto.LabelPair{
				Name:  stringPtr(labelContainer),
				Value: stringPtr(cp.ContainerName),
			}

			appendMetric := func(allowed string, rl corev1.ResourceList) {
				lpAllowed := &dto.LabelPair{
					Name:  stringPtr(labelAllowed),
					Value: stringPtr(allowed),
				}
				for k := range rl {
					v := rl[k]
					pv := &v
					m := &dto.Metric{
						Label: []*dto.LabelPair{
							lpName,
							lpNamespace,
							lpContainer,
							lpAllowed,
							&dto.LabelPair{
								Name:  stringPtr(labelResource),
								Value: stringPtr(string(k)),
							},
							lpTargetRefKind,
							lpTargetRefName,
						},
					}
					m.Gauge = &dto.Gauge{Value: float64Ptr(float64(pv.Value()))}
					(*mfPtr).Metric = append((*mfPtr).Metric, m)
				}
			}

			if cp.MinAllowed != nil {
				appendMetric(minAllowed, cp.MinAllowed)
			}
			if cp.MaxAllowed != nil {
				appendMetric(maxAllowed, cp.MaxAllowed)
			}
		}
	}
	return
}

func statusMetricFamilyHelper(metric string, getStatusFn func(vpa *vpa.VerticalPodAutoscaler) *vpa.VerticalPodAutoscalerStatus, vs []*vpa.VerticalPodAutoscaler) (valid *dto.MetricFamily, invalid *dto.MetricFamily) {
	if len(vs) < 1 {
		return
	}

	valid = &dto.MetricFamily{
		Name: stringPtr(prometheus.BuildFQName(metric, subsystemStatus, "recommendation")),
		Type: metricType(dto.MetricType_GAUGE),
	}
	invalid = &dto.MetricFamily{
		Name: stringPtr(prometheus.BuildFQName(metric, subsystemStatus, "recommendation")),
		Type: metricType(dto.MetricType_GAUGE),
	}

	for _, v := range vs {
		var (
			targetRefKind string
			targetRefName string
			isInvalid     bool
		)

		if v.Spec.TargetRef == nil {
			isInvalid = true
		} else {
			targetRefKind = v.Spec.TargetRef.Kind
			targetRefName = v.Spec.TargetRef.Name
		}

		lpName := &dto.LabelPair{
			Name:  stringPtr(labelName),
			Value: stringPtr(v.Name),
		}
		lpNamespace := &dto.LabelPair{
			Name:  stringPtr(labelNamespace),
			Value: stringPtr(v.Namespace),
		}
		lpTargetRefKind := &dto.LabelPair{
			Name:  stringPtr(labelTargetRefKind),
			Value: stringPtr(targetRefKind),
		}
		lpTargetRefName := &dto.LabelPair{
			Name:  stringPtr(labelTargetRefName),
			Value: stringPtr(targetRefName),
		}
		lpUpdatePolicy := &dto.LabelPair{
			Name: stringPtr(labelUpdatePolicy),
			Value: stringPtr((func() string {
				if v.Spec.UpdatePolicy != nil && v.Spec.UpdatePolicy.UpdateMode != nil {
					return string(*v.Spec.UpdatePolicy.UpdateMode)
				} else {
					return string(vpa.UpdateModeAuto)
				}
			})()),
		}

		status := getStatusFn(v)
		if status.Recommendation == nil || status.Recommendation.ContainerRecommendations == nil {
			m := &dto.Metric{
				Label: []*dto.LabelPair{
					lpName,
					lpNamespace,
					lpTargetRefKind,
					lpTargetRefName,
					lpUpdatePolicy,
				},
			}
			invalid.Metric = append(invalid.Metric, m)

			continue
		}

		var mfPtr **dto.MetricFamily
		if isInvalid {
			mfPtr = &invalid
		} else {
			mfPtr = &valid
		}

		for i := range status.Recommendation.ContainerRecommendations {
			cr := &status.Recommendation.ContainerRecommendations[i]
			lpContainer := &dto.LabelPair{
				Name:  stringPtr(labelContainer),
				Value: stringPtr(cr.ContainerName),
			}

			appendMetric := func(recommendation string, rl corev1.ResourceList) {
				lpRecommendation := &dto.LabelPair{
					Name:  stringPtr(labelRecommendation),
					Value: stringPtr(recommendation),
				}
				for k := range rl {
					v := rl[k]
					pv := &v
					m := &dto.Metric{
						Label: []*dto.LabelPair{
							lpName,
							lpNamespace,
							lpContainer,
							lpRecommendation,
							&dto.LabelPair{
								Name:  stringPtr(labelResource),
								Value: stringPtr(string(k)),
							},
							lpTargetRefKind,
							lpTargetRefName,
							lpUpdatePolicy,
						},
					}
					if k == corev1.ResourceCPU {
						m.Gauge = &dto.Gauge{Value: float64Ptr(float64(pv.MilliValue()))}
					} else {
						m.Gauge = &dto.Gauge{Value: float64Ptr(float64(pv.Value()))}
					}
					(*mfPtr).Metric = append((*mfPtr).Metric, m)
				}
			}

			if cr.Target != nil {
				appendMetric(targetRecommendation, cr.Target)
			}
			if cr.LowerBound != nil {
				appendMetric(lowerBoundRecommendation, cr.LowerBound)
			}
			if cr.UpperBound != nil {
				appendMetric(upperBoundRecommendation, cr.UpperBound)
			}
			if cr.UncappedTarget != nil {
				appendMetric(uncappedTargetRecommendation, cr.UncappedTarget)
			}
		}
	}
	return
}

func statusAnnotationsRecommendationMetricFamily(vs ...*vpa.VerticalPodAutoscaler) (valid *dto.MetricFamily, invalid *dto.MetricFamily) {
	return statusMetricFamilyHelper(gardener_vpa, func(v *vpa.VerticalPodAutoscaler) *vpa.VerticalPodAutoscalerStatus {
		gardenerVPAStatus := &vpa.VerticalPodAutoscalerStatus{}
		gardenerVPAStatusJSON, hasGardenerVPAStatusJSON := v.ObjectMeta.Annotations[gardenerVPARecommendationAnnnotationKey]
		if hasGardenerVPAStatusJSON && gardenerVPAStatusJSON != "" {
			gardenerVPAStatus := &vpa.VerticalPodAutoscalerStatus{}
			err := json.Unmarshal([]byte(gardenerVPAStatusJSON), gardenerVPAStatus)
			if err != nil {
				fmt.Println("error in unmarshalling gardenerVPAStatus")
				return nil
			}
			return gardenerVPAStatus
		}
		return gardenerVPAStatus
	}, vs)
}

func statusRecommendationMetricFamily(vs ...*vpa.VerticalPodAutoscaler) (valid *dto.MetricFamily, invalid *dto.MetricFamily) {

	return statusMetricFamilyHelper(vpaNamespace, func(vpa *vpa.VerticalPodAutoscaler) *vpa.VerticalPodAutoscalerStatus {
		return &vpa.Status
	}, vs)
}

func matchLabels(expect []*dto.LabelPair, actual []*dto.LabelPair) bool {
	if len(expect) > 0 && len(actual) < 1 {
		return false
	}

	am := make(map[string]string, len(actual))
	for _, ap := range actual {
		am[ap.GetName()] = ap.GetValue()
	}

	for _, ep := range expect {
		if av, ok := am[ep.GetName()]; !ok || av != ep.GetValue() {
			return false
		}
	}

	return true
}

func mergeMetricFamily(mfs map[string]*dto.MetricFamily, nmf *dto.MetricFamily) {
	if nmf == nil || len(nmf.Metric) < 1 {
		return
	}

	if omf, ok := mfs[nmf.GetName()]; ok {
		omf.Metric = append(omf.Metric, nmf.Metric...)
	} else {
		mfs[nmf.GetName()] = nmf
	}
}

func getActualMetricFamilies() (map[string]*dto.MetricFamily, error) {
	amfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return nil, err
	}

	m := make(map[string]*dto.MetricFamily, len(amfs))
	for _, amf := range amfs {
		mergeMetricFamily(m, amf)
	}

	return m, nil
}

func (f *fixture) expectMetricFamilies(emfs map[string]*dto.MetricFamily) {
	for _, emf := range emfs {
		f.expectMetricFamily(emf)
	}
}

func (f *fixture) expectMetricFamily(emf *dto.MetricFamily) {
	amfs, err := getActualMetricFamilies()
	if err != nil {
		f.t.Error(err)
		return
	}

	amf, ok := amfs[emf.GetName()]
	if !ok {
		f.t.Errorf("Expected metric family %s but got nothing", emf.GetName())
		return
	}

	if emf.GetType() != amf.GetType() {
		f.t.Errorf("Expected metric family %s to have type %d but got %d", emf.GetName(), emf.GetType(), amf.GetType())
		return
	}

EMFM:
	for _, emfm := range emf.GetMetric() {
		for _, amfm := range amf.GetMetric() {
			if matchLabels(emfm.GetLabel(), amfm.GetLabel()) {
				if emfm.GetGauge().GetValue() != amfm.GetGauge().GetValue() {
					f.t.Errorf("Expected %v but got %v", emfm, amfm)
				}
				continue EMFM
			}
		}

		f.t.Errorf("Expected %v but got nothing", emfm)
	}
}

func (f *fixture) doNotExpectMetricFamilies(emfs map[string]*dto.MetricFamily) {
	for _, emf := range emfs {
		f.doNotExpectMetricFamily(emf)
	}
}

func (f *fixture) doNotExpectMetricFamily(emf *dto.MetricFamily) {
	amfs, err := getActualMetricFamilies()
	if err != nil {
		f.t.Error(err)
		return
	}

	amf, ok := amfs[emf.GetName()]
	if !ok {
		return
	}

	for _, emfm := range emf.GetMetric() {
		for _, amfm := range amf.GetMetric() {
			if matchLabels(emfm.GetLabel(), amfm.GetLabel()) {
				if emfm.GetGauge() == nil || emfm.GetGauge().GetValue() == amfm.GetGauge().GetValue() {
					f.t.Errorf("Did not expect %v but got %v", emfm, amfm)
				}
			}
		}
	}
}

func computeExpectedMetricFamilies(vs ...*vpa.VerticalPodAutoscaler) (valid map[string]*dto.MetricFamily, invalid map[string]*dto.MetricFamily) {
	if len(vs) < 1 {
		return nil, nil
	}

	valid = make(map[string]*dto.MetricFamily)
	invalid = make(map[string]*dto.MetricFamily)
	for _, v := range vs {
		mergeMetricFamily(valid, metadataGenerationMetricFamily(v))

		vmfs, ivmfs := specContainerResourcePolicyAllowedMetricFamily(v)
		mergeMetricFamily(valid, vmfs)
		mergeMetricFamily(invalid, ivmfs)

		vmfs, ivmfs = statusRecommendationMetricFamily(v)
		mergeMetricFamily(valid, vmfs)
		mergeMetricFamily(invalid, ivmfs)

		vmfs, ivmfs = statusAnnotationsRecommendationMetricFamily(v)
		mergeMetricFamily(valid, vmfs)
		mergeMetricFamily(invalid, ivmfs)
	}

	return
}

func testRunVPA(t *testing.T, modifierFunc func(f *fixture) (valid map[string]*dto.MetricFamily, invalid map[string]*dto.MetricFamily), vs ...*vpa.VerticalPodAutoscaler) {
	f, err := newFixture(t, vs...)
	if err != nil {
		t.Errorf("Expected no error while creating the test fixture but got %s", err)
		return
	}
	defer f.stop()

	resetMetrics(true)
	go f.run()

	if ok := cache.WaitForCacheSync(f.stopCh, f.vpasSynced); !ok {
		t.Error("Error waiting for vpas to sync")
		return
	}
	// wait for the controller to take action
	time.Sleep(1 * time.Second)

	vmfs, ivmfs := computeExpectedMetricFamilies(vs...)
	f.expectMetricFamilies(vmfs)
	f.doNotExpectMetricFamilies(ivmfs)

	if modifierFunc != nil {
		vmfs, ivmfs = modifierFunc(f)

		if ok := cache.WaitForCacheSync(f.stopCh, f.vpasSynced); !ok {
			t.Error("Error waiting for vpas to sync")
			return
		}
		// wait for the controller to take action
		time.Sleep(1 * time.Second)

		f.expectMetricFamilies(vmfs)
		f.doNotExpectMetricFamilies(ivmfs)
	}
}

func TestRunValidVPA(t *testing.T) {
	const name = "v"

	gardenerVPAStatus := &vpa.VerticalPodAutoscalerStatus{
		Recommendation: &vpa.RecommendedPodResources{
			ContainerRecommendations: []vpa.RecommendedContainerResources{
				vpa.RecommendedContainerResources{
					ContainerName:  name,
					LowerBound:     getResources("100m", "100Mi"),
					Target:         getResources("150m", "150Mi"),
					UpperBound:     getResources("200m", "200Mi"),
					UncappedTarget: getResources("300m", "300Mi"),
				},
			},
		},
	}

	gardenerVPAStatusJSON, err := json.Marshal(gardenerVPAStatus)
	if err != nil {
		t.Error(err)
		return
	}

	v := &vpa.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "valid",
			Namespace:  name,
			Generation: 1,
			Annotations: map[string]string{
				gardenerVPARecommendationAnnnotationKey: string(gardenerVPAStatusJSON),
			},
		},
		Spec: vpa.VerticalPodAutoscalerSpec{
			TargetRef: &autoscaling.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name,
			},
			UpdatePolicy: podUpdatePolicy(vpa.UpdateModeOff),
			ResourcePolicy: &vpa.PodResourcePolicy{
				ContainerPolicies: []vpa.ContainerResourcePolicy{
					vpa.ContainerResourcePolicy{
						ContainerName: name,
						MinAllowed:    getResources("100m", "100Mi"),
						MaxAllowed:    getResources("200m", "200Mi"),
						Mode:          containerScalingMode(vpa.ContainerScalingModeAuto),
					},
				},
			},
		},
		Status: vpa.VerticalPodAutoscalerStatus{
			Recommendation: &vpa.RecommendedPodResources{
				ContainerRecommendations: []vpa.RecommendedContainerResources{
					vpa.RecommendedContainerResources{
						ContainerName:  name,
						LowerBound:     getResources("100m", "100Mi"),
						Target:         getResources("150m", "150Mi"),
						UpperBound:     getResources("200m", "200Mi"),
						UncappedTarget: getResources("300m", "300Mi"),
					},
				},
			},
		},
	}

	testRunVPA(t, nil, v)
}

func TestRunInvalidVPA(t *testing.T) {
	const name = "v"

	gardenerVPAStatus := &vpa.VerticalPodAutoscalerStatus{
		Recommendation: &vpa.RecommendedPodResources{
			ContainerRecommendations: []vpa.RecommendedContainerResources{
				vpa.RecommendedContainerResources{
					ContainerName:  name,
					LowerBound:     getResources("100m", "100Mi"),
					Target:         getResources("150m", "150Mi"),
					UpperBound:     getResources("200m", "200Mi"),
					UncappedTarget: getResources("300m", "300Mi"),
				},
			},
		},
	}

	gardenerVPAStatusJSON, err := json.Marshal(gardenerVPAStatus)
	if err != nil {
		t.Error(err)
		return
	}

	empty := &vpa.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "empty",
			Namespace:  name,
			Generation: 1,
			Annotations: map[string]string{
				gardenerVPARecommendationAnnnotationKey: string(gardenerVPAStatusJSON),
			},
		},
	}
	noContainers := &vpa.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "noContainers",
			Namespace:  name,
			Generation: 1,
			Annotations: map[string]string{
				gardenerVPARecommendationAnnnotationKey: string(gardenerVPAStatusJSON),
			},
		},
		Spec: vpa.VerticalPodAutoscalerSpec{
			ResourcePolicy: &vpa.PodResourcePolicy{},
		},
		Status: vpa.VerticalPodAutoscalerStatus{
			Recommendation: &vpa.RecommendedPodResources{},
		},
	}
	noContainersWithTargetRef := &vpa.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "noContainersWithTargetRef",
			Namespace:  name,
			Generation: 1,
			Annotations: map[string]string{
				gardenerVPARecommendationAnnnotationKey: string(gardenerVPAStatusJSON),
			},
		},
		Spec: vpa.VerticalPodAutoscalerSpec{
			UpdatePolicy: podUpdatePolicy(vpa.UpdateModeOff),
			TargetRef: &autoscaling.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name,
			},
		},
	}
	noTargetRefWithContainers := &vpa.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "noTargetRefWithContainers",
			Namespace:  name,
			Generation: 1,
			Annotations: map[string]string{
				gardenerVPARecommendationAnnnotationKey: string(gardenerVPAStatusJSON),
			},
		},
		Spec: vpa.VerticalPodAutoscalerSpec{
			UpdatePolicy: podUpdatePolicy(vpa.UpdateModeOff),
			ResourcePolicy: &vpa.PodResourcePolicy{
				ContainerPolicies: []vpa.ContainerResourcePolicy{
					vpa.ContainerResourcePolicy{
						ContainerName: name,
						MinAllowed:    getResources("100m", "100Mi"),
						MaxAllowed:    getResources("200m", "200Mi"),
						Mode:          containerScalingMode(vpa.ContainerScalingModeAuto),
					},
				},
			},
		},
		Status: vpa.VerticalPodAutoscalerStatus{
			Recommendation: &vpa.RecommendedPodResources{
				ContainerRecommendations: []vpa.RecommendedContainerResources{
					vpa.RecommendedContainerResources{
						ContainerName:  name,
						LowerBound:     getResources("100m", "100Mi"),
						Target:         getResources("150m", "150Mi"),
						UpperBound:     getResources("200m", "200Mi"),
						UncappedTarget: getResources("300m", "300Mi"),
					},
				},
			},
		},
	}

	testRunVPA(t, nil, empty, noContainers, noContainersWithTargetRef, noTargetRefWithContainers)
}

func TestRunMutatingVPA(t *testing.T) {
	const name = "v"

	gardenerVPAStatus := &vpa.VerticalPodAutoscalerStatus{
		Recommendation: &vpa.RecommendedPodResources{
			ContainerRecommendations: []vpa.RecommendedContainerResources{
				vpa.RecommendedContainerResources{
					ContainerName:  name,
					LowerBound:     getResources("100m", "100Mi"),
					Target:         getResources("150m", "150Mi"),
					UpperBound:     getResources("200m", "200Mi"),
					UncappedTarget: getResources("300m", "300Mi"),
				},
			},
		},
	}

	gardenerVPAStatusJSON, err := json.Marshal(gardenerVPAStatus)
	if err != nil {
		t.Error(err)
		return
	}

	v := &vpa.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mutating",
			Namespace:  name,
			Generation: 1,
			Annotations: map[string]string{
				gardenerVPARecommendationAnnnotationKey: string(gardenerVPAStatusJSON),
			},
		},
		Spec: vpa.VerticalPodAutoscalerSpec{
			TargetRef: &autoscaling.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name,
			},
			UpdatePolicy: podUpdatePolicy(vpa.UpdateModeOff),
			ResourcePolicy: &vpa.PodResourcePolicy{
				ContainerPolicies: []vpa.ContainerResourcePolicy{
					vpa.ContainerResourcePolicy{
						ContainerName: name,
						MinAllowed:    getResources("100m", "100Mi"),
						MaxAllowed:    getResources("200m", "200Mi"),
						Mode:          containerScalingMode(vpa.ContainerScalingModeAuto),
					},
				},
			},
		},
		Status: vpa.VerticalPodAutoscalerStatus{
			Recommendation: &vpa.RecommendedPodResources{
				ContainerRecommendations: []vpa.RecommendedContainerResources{
					vpa.RecommendedContainerResources{
						ContainerName:  name,
						LowerBound:     getResources("100m", "100Mi"),
						Target:         getResources("150m", "150Mi"),
						UpperBound:     getResources("200m", "200Mi"),
						UncappedTarget: getResources("300m", "300Mi"),
					},
				},
			},
		},
	}

	modifierFunc := func(f *fixture) (valid map[string]*dto.MetricFamily, invalid map[string]*dto.MetricFamily) {
		vc := v.DeepCopy()
		vc.ResourceVersion = time.Now().String()

		gardenerVPAStatus := &vpa.VerticalPodAutoscalerStatus{
			Recommendation: &vpa.RecommendedPodResources{
				ContainerRecommendations: []vpa.RecommendedContainerResources{
					vpa.RecommendedContainerResources{
						ContainerName:  name,
						LowerBound:     getResources("101m", "101Mi"),
						Target:         getResources("151m", "151Mi"),
						UpperBound:     getResources("201m", "201Mi"),
						UncappedTarget: getResources("301m", "301Mi"),
					},
				},
			},
		}

		gardenerVPAStatusJSON, err1 := json.Marshal(gardenerVPAStatus)
		if err1 != nil {
			fmt.Println(err1)
			return
		}
		vc.ObjectMeta.Annotations[gardenerVPARecommendationAnnnotationKey] = string(gardenerVPAStatusJSON)

		vc.Status.Recommendation.ContainerRecommendations[0].LowerBound = getResources("101m", "101Mi")
		vc.Status.Recommendation.ContainerRecommendations[0].Target = getResources("151m", "151Mi")
		vc.Status.Recommendation.ContainerRecommendations[0].UpperBound = getResources("201m", "201Mi")
		vc.Status.Recommendation.ContainerRecommendations[0].UncappedTarget = getResources("301m", "301Mi")
		_, err := f.client.AutoscalingV1beta2().VerticalPodAutoscalers(v.Namespace).Update(vc)
		if err != nil {
			t.Error(err)
			return
		}
		f.watcher.Add(vc)

		valid, invalid = computeExpectedMetricFamilies(vc)
		oldStatusVMFS, _ := statusRecommendationMetricFamily(v)
		oldAnnotationStatusVMFS, _ := statusAnnotationsRecommendationMetricFamily(v)
		mergeMetricFamily(invalid, oldStatusVMFS)
		mergeMetricFamily(invalid, oldAnnotationStatusVMFS)
		return
	}

	testRunVPA(t, modifierFunc, v)
}

/*
 * TODO handle vpa deletion properly
func TestRunDeleteVPA(t *testing.T) {
	const name = "v"
	v := &vpa.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mutating",
			Namespace:  name,
			Generation: 1,
		},
		Spec: vpa.VerticalPodAutoscalerSpec{
			TargetRef: &autoscaling.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name,
			},
			UpdatePolicy: podUpdatePolicy(vpa.UpdateModeOff),
			ResourcePolicy: &vpa.PodResourcePolicy{
				ContainerPolicies: []vpa.ContainerResourcePolicy{
					vpa.ContainerResourcePolicy{
						ContainerName: name,
						MinAllowed:    getResources("100m", "100Mi"),
						MaxAllowed:    getResources("200m", "200Mi"),
						Mode:          containerScalingMode(vpa.ContainerScalingModeAuto),
					},
				},
			},
		},
		Status: vpa.VerticalPodAutoscalerStatus{
			Recommendation: &vpa.RecommendedPodResources{
				ContainerRecommendations: []vpa.RecommendedContainerResources{
					vpa.RecommendedContainerResources{
						ContainerName:  name,
						LowerBound:     getResources("100m", "100Mi"),
						Target:         getResources("150m", "150Mi"),
						UpperBound:     getResources("200m", "200Mi"),
						UncappedTarget: getResources("300m", "300Mi"),
					},
				},
			},
		},
	}

	modifierFunc := func(f *fixture) (valid map[string]*dto.MetricFamily, invalid map[string]*dto.MetricFamily) {
		err := f.client.AutoscalingV1beta2().VerticalPodAutoscalers(v.Namespace).Delete(v.Name, &metav1.DeleteOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		f.watcher.Delete(v)

		// after deletion valid is invalid and vice versa
		invalid, valid = computeExpectedMetricFamilies(v)
		return
	}

	testRunVPA(t, modifierFunc, v)
}
*/
