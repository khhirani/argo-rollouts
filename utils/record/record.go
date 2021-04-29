package record

import (
	"encoding/json"
	"fmt"

	"github.com/argoproj/argo-rollouts/utils/conditions"
	"github.com/argoproj/notifications-engine/pkg"
	notificationsController "github.com/argoproj/notifications-engine/pkg/controller"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubectl/pkg/scheme"

	logutil "github.com/argoproj/argo-rollouts/utils/log"
)

const (
	controllerAgentName   = "rollouts-controller"
	NotificationConfigMap = "argo-rollouts-notification-configmap"
	NotificationSecret    = "argo-rollouts-notification-secret"
)

var (
	BuiltInTriggers = map[string]string{
		//"on-paused":    conditions.PausedRolloutReason,
		"on-completed":          conditions.RolloutCompletedReason,
		"on-step-completed":     conditions.RolloutStepCompletedReason,
		"on-scaling-replicaset": conditions.ScalingReplicaSetReason,
		"on-update":             conditions.RolloutUpdatedReason,
	}
	EventReasonToTrigger = reverseMap(BuiltInTriggers)
)

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
	GetAPI() (pkg.API, map[string][]string, error)
}

// EventRecorderAdapter implements the EventRecorder interface
type EventRecorderAdapter struct {
	// Recorder is a K8s EventRecorder
	Recorder record.EventRecorder
	// TODO: add comment
	// ConfigMapInformer to retrieve NotificationConfigMap
	cmInformer coreinformers.ConfigMapInformer
	// SecretInformer to retrieve NotificationSecret
	secretInformer coreinformers.SecretInformer
	namespace      string
}

func NewEventRecorder(kubeclientset kubernetes.Interface, namespace string, cmInformer coreinformers.ConfigMapInformer, secretInformer coreinformers.SecretInformer) EventRecorder {
	// Create event broadcaster
	// Add argo-rollouts custom resources to the default Kubernetes Scheme so Events can be
	// logged for argo-rollouts types.
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(log.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	return &EventRecorderAdapter{
		Recorder:       recorder,
		cmInformer:     cmInformer,
		secretInformer: secretInformer,
		namespace:      namespace,
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
	subsFromAnnotations := notificationsController.Subscriptions(object.(metav1.Object).GetAnnotations())
	subsByTrigger := subsFromAnnotations.GetAll(nil, map[string][]string{})

	trigger, ok := EventReasonToTrigger[opts.EventReason]
	if !ok {
		return nil
	}

	destinations := subsByTrigger[trigger]
	if len(destinations) == 0 {
		return nil
	}

	api, templates, err := e.GetAPI()
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
			log.Error("notification error: %s", err.Error())
			return err
		}
	}
	return nil
}

func (e *EventRecorderAdapter) K8sRecorder() record.EventRecorder {
	return e.Recorder
}

func (e *EventRecorderAdapter) GetAPI() (pkg.API, map[string][]string, error) {
	configMapKey := fmt.Sprintf("%s/%s", e.namespace, NotificationConfigMap)
	configMap, exists, err := e.cmInformer.Informer().GetStore().GetByKey(configMapKey)
	if !exists {
		return nil, nil, fmt.Errorf("notification configMap %s does not exist", NotificationConfigMap)
	}
	if err != nil {
		return nil, nil, err
	}

	// optional
	// only necessary if notification configmap references secret
	secretKey := fmt.Sprintf("%s/%s", e.namespace, NotificationSecret)
	secret, secretExists, err := e.secretInformer.Informer().GetStore().GetByKey(secretKey)
	if !secretExists {
		log.Warnf("notification secret %s does not exist", NotificationSecret)
		secret = &corev1.Secret{}
		//return nil, nil, fmt.Errorf("notification secret %s does not exist", NotificationSecret)
	}
	if err != nil {
		return nil, nil, err
	}

	// Creates config for notifications for built-in triggers
	templates := map[string][]string{}
	cfg, err := pkg.ParseConfig(configMap.(*corev1.ConfigMap), secret.(*corev1.Secret))
	for name, triggers := range cfg.Triggers {
		if _, ok := BuiltInTriggers[name]; ok {
			templates[name] = triggers[0].Send
			delete(cfg.Triggers, name)
		}
	}
	if err != nil {
		return nil, nil, err
	}

	api, err := pkg.NewAPI(*cfg)

	// TODO: Cache API
	return api, templates, err
}

func reverseMap(m map[string]string) map[string]string {
	n := make(map[string]string)
	for k, v := range m {
		n[v] = k
	}
	return n
}
