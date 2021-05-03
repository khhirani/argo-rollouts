package record

import (
	"encoding/json"

	"github.com/argoproj/argo-rollouts/utils/conditions"
	logutil "github.com/argoproj/argo-rollouts/utils/log"

	"github.com/argoproj/notifications-engine/pkg/api"
	"github.com/argoproj/notifications-engine/pkg/services"
	"github.com/argoproj/notifications-engine/pkg/subscriptions"
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
)

const (
	controllerAgentName   = "rollouts-controller"
	NotificationConfigMap = "argo-rollouts-notification-configmap"
	NotificationSecret    = "argo-rollouts-notification-secret"
)

var (
	BuiltInTriggers = map[string]string{
		"on-completed":          conditions.RolloutCompletedReason,
		"on-step-completed":     conditions.RolloutStepCompletedReason,
		"on-scaling-replicaset": conditions.ScalingReplicaSetReason,
		"on-update":             conditions.RolloutUpdatedReason,
	}
	EventReasonToTrigger = reverseMap(BuiltInTriggers)
)

func NewAPIFactorySettings() api.Settings {
	return api.Settings{
		SecretName:    NotificationSecret,
		ConfigMapName: NotificationConfigMap,
		InitGetVars: func(cfg *api.Config, configMap *corev1.ConfigMap, secret *corev1.Secret) (api.GetVars, error) {
			return func(obj map[string]interface{}, dest services.Destination) map[string]interface{} {
				return map[string]interface{}{"rollout": obj}
			}, nil
		},
	}
}

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
	GetAPIFactory() api.Factory
}

// EventRecorderAdapter implements the EventRecorder interface
type EventRecorderAdapter struct {
	// Recorder is a K8s EventRecorder
	Recorder   record.EventRecorder
	apiFactory api.Factory
}

func NewEventRecorder(kubeclientset kubernetes.Interface, apiFactory api.Factory) EventRecorder {
	// Create event broadcaster
	// Add argo-rollouts custom resources to the default Kubernetes Scheme so Events can be
	// logged for argo-rollouts types.
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(log.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	return &EventRecorderAdapter{
		Recorder:   recorder,
		apiFactory: apiFactory,
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

	return e.sendNotifications(object, opts)
}

// Send notifications for triggered event if user is subscribed
func (e *EventRecorderAdapter) sendNotifications(object runtime.Object, opts EventOptions) error {
	subsFromAnnotations := subscriptions.Annotations(object.(metav1.Object).GetAnnotations())
	destByTrigger := subsFromAnnotations.GetDestinations(nil, map[string][]string{})

	trigger, ok := EventReasonToTrigger[opts.EventReason]
	if !ok {
		return nil
	}

	destinations := destByTrigger[trigger]
	if len(destinations) == 0 {
		return nil
	}

	notificationsAPI, err := e.apiFactory.GetAPI()
	if err != nil {
		return err
	}

	// Creates config for notifications for built-in triggers
	templates := map[string][]string{}
	for name, triggers := range notificationsAPI.GetConfig().Triggers {
		if _, ok := BuiltInTriggers[name]; ok {
			templates[name] = triggers[0].Send
		}
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
		err = notificationsAPI.Send(vars, templates[trigger], dest)
		if err != nil {
			log.Error("notification error: %s", err.Error())
			return err
		}
	}
	return nil
}

func (e *EventRecorderAdapter) K8sRecorder() record.EventRecorder {
	return e.Recorder
}

func (e *EventRecorderAdapter) GetAPIFactory() api.Factory {
	return e.apiFactory
}

func reverseMap(m map[string]string) map[string]string {
	n := make(map[string]string)
	for k, v := range m {
		n[v] = k
	}
	return n
}
