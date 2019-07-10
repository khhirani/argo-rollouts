package experiments

import (
	"reflect"
	"testing"
	"time"

	"github.com/bouk/monkey"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/argoproj/argo-rollouts/controller/metrics"
	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/argoproj/argo-rollouts/pkg/client/clientset/versioned/fake"
	informers "github.com/argoproj/argo-rollouts/pkg/client/informers/externalversions"
	"github.com/argoproj/argo-rollouts/utils/annotations"
)

var (
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

const (
	OnlyObservedGenerationPatch = `{
			"status" : {
				"observedGeneration": ""
			}
	}`
)

type fixture struct {
	t *testing.T

	client     *fake.Clientset
	kubeclient *k8sfake.Clientset
	// Objects to put in the store.
	// rolloutLister    []*v1alpha1.Rollout
	experimentLister []*v1alpha1.Experiment
	// replicaSetLister []*appsv1.ReplicaSet
	// Actions expected to happen on the client.
	kubeactions []core.Action
	actions     []core.Action
	// Objects from here preloaded into NewSimpleFake.
	kubeobjects     []runtime.Object
	objects         []runtime.Object
	enqueuedObjects map[string]int
	unfreezeTime    func()
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	f.enqueuedObjects = make(map[string]int)
	now := time.Now()
	patch := monkey.Patch(time.Now, func() time.Time { return now })
	f.unfreezeTime = patch.Unpatch
	return f
}

func (f *fixture) Close() {
	f.unfreezeTime()
}

func newExperiment(name string, replicas int, revisionHistoryLimit *int32, selector map[string]string) *v1alpha1.Experiment {
	ex := &v1alpha1.Experiment{
		ObjectMeta: metav1.ObjectMeta{
			UID:       uuid.NewUUID(),
			Name:      name,
			Namespace: metav1.NamespaceDefault,
			Annotations: map[string]string{
				annotations.RevisionAnnotation: "1",
			},
		},
		Spec: v1alpha1.ExperimentSpec{
			Templates: []v1alpha1.TemplateSpec{
				{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: selector,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Image: "foo/bar",
								},
							},
						},
					},
				},
			},
		},
		Status: v1alpha1.ExperimentStatus{},
	}
	return ex
}

func getKey(experiment *v1alpha1.Experiment, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(experiment)
	if err != nil {
		t.Errorf("Unexpected error getting key for experiment %v: %v", experiment.Name, err)
		return ""
	}
	return key
}

type resyncFunc func() time.Duration

func (f *fixture) newController(resync resyncFunc) (*ExperimentController, informers.SharedInformerFactory, kubeinformers.SharedInformerFactory) {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)

	i := informers.NewSharedInformerFactory(f.client, resync())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, resync())

	rolloutWorkqueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Rollouts")
	experimentWorkqueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Experiments")

	c := NewExperimentController(f.kubeclient, f.client,
		k8sI.Apps().V1().ReplicaSets(),
		i.Argoproj().V1alpha1().Rollouts(),
		i.Argoproj().V1alpha1().Experiments(),
		resync(),
		rolloutWorkqueue,
		experimentWorkqueue,
		metrics.NewMetricsServer("localhost:8080", i.Argoproj().V1alpha1().Rollouts().Lister()),
		&record.FakeRecorder{})

	c.enqueueExperiment = func(obj interface{}) {
		var key string
		var err error
		if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
			panic(err)
		}
		count, ok := f.enqueuedObjects[key]
		if !ok {
			count = 0
		}
		count++
		f.enqueuedObjects[key] = count
		c.enqueue(obj)
	}
	c.enqueueExperimentAfter = func(obj interface{}, duration time.Duration) {
		c.enqueueExperiment(obj)
	}

	//for _, r := range f.experimentLister {
	//	i.Argoproj().V1alpha1().Experiments().Informer().GetIndexer().Add(r)
	//}
	//
	//for _, r := range f.rolloutLister {
	//	i.Argoproj().V1alpha1().Rollouts().Informer().GetIndexer().Add(r)
	//}
	//
	//for _, r := range f.replicaSetLister {
	//	k8sI.Apps().V1().ReplicaSets().Informer().GetIndexer().Add(r)
	//}

	return c, i, k8sI
}

func (f *fixture) run(experimentName string) {
	c, i, k8sI := f.newController(noResyncPeriodFunc)
	f.runController(experimentName, true, false, c, i, k8sI)
}

func (f *fixture) runExpectError(experimentName string, startInformers bool) {
	c, i, k8sI := f.newController(noResyncPeriodFunc)
	f.runController(experimentName, startInformers, true, c, i, k8sI)
}

func (f *fixture) runController(experimentName string, startInformers bool, expectError bool, c *ExperimentController, i informers.SharedInformerFactory, k8sI kubeinformers.SharedInformerFactory) *ExperimentController {
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		i.Start(stopCh)
		k8sI.Start(stopCh)

		assert.True(f.t, cache.WaitForCacheSync(stopCh, c.replicaSetSynced, c.rolloutSynced, c.experimentSynced))
	}

	err := c.syncHandler(experimentName)
	if !expectError && err != nil {
		f.t.Errorf("error syncing experiment: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing experiment, got nil")
	}

	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		if len(f.actions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.actions), actions[i:])
			break
		}

		expectedAction := f.actions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.actions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.actions)-len(actions), f.actions[len(actions):])
	}

	k8sActions := filterInformerActions(f.kubeclient.Actions())
	for i, action := range k8sActions {
		if len(f.kubeactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(k8sActions)-len(f.kubeactions), k8sActions[i:])
			break
		}

		expectedAction := f.kubeactions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.kubeactions) > len(k8sActions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.kubeactions)-len(k8sActions), f.kubeactions[len(k8sActions):])
	}
	return c
}

// checkAction verifies that expected and actual actions are equal
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		if patch, ok := actual.(core.PatchAction); ok {
			patchBytes := patch.GetPatch()
			t.Errorf("Patch Received: %s", string(patchBytes))
		}
		if patch, ok := expected.(core.PatchAction); ok {
			patchBytes := patch.GetPatch()
			t.Errorf("Expected Patch: %s", string(patchBytes))
		}
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}
}

// filterInformerActions filters list, and watch actions for testing resources.
// Since list, and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if action.Matches("list", "rollouts") ||
			action.Matches("watch", "rollouts") ||
			action.Matches("list", "replicaSets") ||
			action.Matches("watch", "replicaSets") ||
			action.Matches("list", "experiments") ||
			action.Matches("watch", "experiments") {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func (f *fixture) expectCreateReplicaSetAction(r *appsv1.ReplicaSet) int {
	len := len(f.kubeactions)
	f.kubeactions = append(f.kubeactions, core.NewCreateAction(schema.GroupVersionResource{Resource: "replicasets"}, r.Namespace, r))
	return len
}

func (f *fixture) expectUpdateReplicaSetAction(r *appsv1.ReplicaSet) int {
	len := len(f.kubeactions)
	f.kubeactions = append(f.kubeactions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "replicasets"}, r.Namespace, r))
	return len
}

func (f *fixture) expectGetRolloutAction(rollout *v1alpha1.Rollout) int {
	len := len(f.kubeactions)
	f.kubeactions = append(f.kubeactions, core.NewGetAction(schema.GroupVersionResource{Resource: "rollouts"}, rollout.Namespace, rollout.Name))
	return len
}

func (f *fixture) expectGetExperimentAction(experiment *v1alpha1.Experiment) int {
	len := len(f.kubeactions)
	f.kubeactions = append(f.kubeactions, core.NewGetAction(schema.GroupVersionResource{Resource: "experiments"}, experiment.Namespace, experiment.Name))
	return len
}

func (f *fixture) expectUpdateExperimentAction(experiment *v1alpha1.Experiment) int {
	action := core.NewUpdateAction(schema.GroupVersionResource{Resource: "experiments"}, experiment.Namespace, experiment)
	len := len(f.actions)
	f.actions = append(f.actions, action)
	return len
}

func (f *fixture) expectPatchExperimentAction(experiment *v1alpha1.Experiment) int {
	serviceSchema := schema.GroupVersionResource{
		Resource: "experiments",
		Version:  "v1alpha1",
	}
	len := len(f.actions)
	f.actions = append(f.actions, core.NewPatchAction(serviceSchema, experiment.Namespace, experiment.Name, types.MergePatchType, nil))
	return len
}

func (f *fixture) getCreatedReplicaSet(index int) *appsv1.ReplicaSet {
	action := filterInformerActions(f.kubeclient.Actions())[index]
	createAction, ok := action.(core.CreateAction)
	if !ok {
		assert.Fail(f.t, "Expected Created action, not %s", action.GetVerb())
	}
	obj := createAction.GetObject()
	rs := &appsv1.ReplicaSet{}
	converter := runtime.NewTestUnstructuredConverter(equality.Semantic)
	objMap, _ := converter.ToUnstructured(obj)
	runtime.NewTestUnstructuredConverter(equality.Semantic).FromUnstructured(objMap, rs)
	return rs
}

func (f *fixture) getUpdatedReplicaSet(index int) *appsv1.ReplicaSet {
	action := filterInformerActions(f.kubeclient.Actions())[index]
	updateAction, ok := action.(core.UpdateAction)
	if !ok {
		assert.Fail(f.t, "Expected Update action, not %s", action.GetVerb())
	}
	obj := updateAction.GetObject()
	rs := &appsv1.ReplicaSet{}
	converter := runtime.NewTestUnstructuredConverter(equality.Semantic)
	objMap, _ := converter.ToUnstructured(obj)
	runtime.NewTestUnstructuredConverter(equality.Semantic).FromUnstructured(objMap, rs)
	return rs
}

func (f *fixture) getUpdatedExperiment(index int) *v1alpha1.Experiment {
	action := filterInformerActions(f.client.Actions())[index]
	updateAction, ok := action.(core.UpdateAction)
	if !ok {
		assert.Fail(f.t, "Expected Update action, not %s", action.GetVerb())
	}
	obj := updateAction.GetObject()
	experiment := &v1alpha1.Experiment{}
	converter := runtime.NewTestUnstructuredConverter(equality.Semantic)
	objMap, _ := converter.ToUnstructured(obj)
	runtime.NewTestUnstructuredConverter(equality.Semantic).FromUnstructured(objMap, experiment)
	return experiment
}

func (f *fixture) getPatchedRollout(index int) string {
	action := filterInformerActions(f.client.Actions())[index]
	patchAction, ok := action.(core.PatchAction)
	if !ok {
		f.t.Fatalf("Expected Patch action, not %s", action.GetVerb())
	}
	return string(patchAction.GetPatch())
}

func TestSyncExperiment(t *testing.T) {
	f := newFixture(t)
	defer f.Close()

	e := newExperiment("foo", 1, nil, nil)
	f.experimentLister = append(f.experimentLister, e)
	f.objects = append(f.objects, e)

	f.run(getKey(e, t))
}
