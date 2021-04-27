package record

import (
	"encoding/json"
	"github.com/argoproj/argo-rollouts/notifications"
	notificationsController "github.com/argoproj/notifications-engine/pkg/controller"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubectl/pkg/scheme"

	logutil "github.com/argoproj/argo-rollouts/utils/log"
)

const controllerAgentName = "rollouts-controller"

type EventOptions struct {
	// EventType is the kubernetes event type (Normal or Warning). Defaults to Normal
	EventType string
	// EventReason is a Kubernetes EventReason of why this event is generated.
	// Reason should be short and unique; it  should be in UpperCamelCase format (starting with a
	// capital letter). "reason" will be used to automate handling of events, so imagine people
	// writing switch statements to handle them.
	EventReason string
	// PrometheusCounter is an optional prometheus counter to increment upon recording the event
	PrometheusCounter *prometheus.CounterVec
}

type EventRecorder interface {
	Eventf(object runtime.Object, opts EventOptions, messageFmt string, args ...interface{}) error
	Warnf(object runtime.Object, opts EventOptions, messageFmt string, args ...interface{}) error
	K8sRecorder() record.EventRecorder
}

// EventRecorderAdapter implements the EventRecorder interface
type EventRecorderAdapter struct {
	// Recorder is a K8s EventRecorder
	Recorder record.EventRecorder
	// TODO: add comment
	NotificationsManager notifications.NotificationsManager
}

func NewEventRecorder(kubeclientset kubernetes.Interface, notificationsManager notifications.NotificationsManager) EventRecorder {
	// Create event broadcaster
	// Add argo-rollouts custom resources to the default Kubernetes Scheme so Events can be
	// logged for argo-rollouts types.
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(log.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	return &EventRecorderAdapter{
		Recorder:             recorder,
		NotificationsManager: notificationsManager,
	}
}

func NewFakeEventRecorder() EventRecorder {
	return &EventRecorderAdapter{
		Recorder: &record.FakeRecorder{},
	}
}

func (e *EventRecorderAdapter) Eventf(object runtime.Object, opts EventOptions, messageFmt string, args ...interface{}) error {
	return e.eventf(object, opts.EventType == corev1.EventTypeWarning, opts, messageFmt, args...)
}

func (e *EventRecorderAdapter) Warnf(object runtime.Object, opts EventOptions, messageFmt string, args ...interface{}) error {
	return e.eventf(object, false, opts, messageFmt, args...)
}

func (e *EventRecorderAdapter) eventf(object runtime.Object, warn bool, opts EventOptions, messageFmt string, args ...interface{}) error {
	logCtx := logutil.WithObject(object)
	eventType := corev1.EventTypeNormal
	if warn {
		eventType = corev1.EventTypeWarning
		logCtx.Warnf(messageFmt, args...)
	} else {
		logCtx.Infof(messageFmt, args...)
	}

	if opts.EventReason != "" {
		e.Recorder.Eventf(object, eventType, opts.EventReason, messageFmt, args...)
	}
	if opts.PrometheusCounter != nil {
		objectMeta, err := meta.Accessor(object)
		if err != nil {
			return err
		}
		opts.PrometheusCounter.WithLabelValues(objectMeta.GetNamespace(), objectMeta.GetName()).Inc()
	}

	subsFromAnnotations := notificationsController.Subscriptions(object.(metav1.Object).GetAnnotations())
	subsByTrigger := subsFromAnnotations.GetAll(nil, map[string][]string{})
	trigger, ok := notifications.ConditionToTrigger[opts.EventReason]
	if !ok {
		return nil
	}

	destinations := subsByTrigger[trigger]
	if len(destinations) == 0 {
		return nil
	}
	api, templates, err := e.NotificationsManager.GetAPI()
	if err != nil {
		return err
	}
	objBytes, err := json.Marshal(object)
	if err != nil {
		return err
	}
	var objMap map[string]interface{}
	err = json.Unmarshal(objBytes, &objMap)
	if err != nil {
		return err
	}
	vars := map[string]interface{}{
		"rollout": objMap,
	}
	for _, dest := range destinations {
		err = api.Send(vars, templates[trigger], dest)
		if err != nil {
			log.Error("API Error: %s", err.Error())
			return err
		}
	}
	return nil
}

func (e *EventRecorderAdapter) K8sRecorder() record.EventRecorder {
	return e.Recorder
}
