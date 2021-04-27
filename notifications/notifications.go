package notifications

import (
	"fmt"

	"github.com/argoproj/argo-rollouts/utils/conditions"
	"github.com/argoproj/notifications-engine/pkg"
	v1 "k8s.io/api/core/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
)

const (
	NotificationConfigMap = "argo-rollout-notification-configmap"
	NotificationSecret    = "argo-rollout-notification-secret"
)

var (
	TriggerToCondition = map[string]string{
		"on-paused":    conditions.PausedRolloutReason,
		"on-completed": conditions.RolloutCompletedReason,
	}
	ConditionToTrigger = map[string]string{
		conditions.PausedRolloutReason:    "on-paused",
		conditions.RolloutCompletedReason: "on-completed",
	}
)

type NotificationsManager interface {
	GetAPI() (pkg.API, map[string][]string, error)
}

func NewNotificationsManager(namespace string, cmInformer coreinformers.ConfigMapInformer, secretInformer coreinformers.SecretInformer) *notificationsManager {
	return &notificationsManager{
		namespace:      namespace,
		cmInformer:     cmInformer,
		secretInformer: secretInformer,
	}
}

type notificationsManager struct {
	//api              pkg.API
	namespace      string
	cmInformer     coreinformers.ConfigMapInformer
	secretInformer coreinformers.SecretInformer
}

func (m *notificationsManager) GetAPI() (pkg.API, map[string][]string, error) {
	// init everything
	// Create in main.go
	configMapKey := fmt.Sprintf("%s/%s", m.namespace, NotificationConfigMap)
	configMap, exists, err := m.cmInformer.Informer().GetStore().GetByKey(configMapKey)
	if !exists {
		return nil, nil, fmt.Errorf("Notification ConfigMap does not exist")
	}
	if err != nil {
		return nil, nil, err
	}

	secretKey := fmt.Sprintf("%s/%s", m.namespace, NotificationSecret)
	secret, exists, err := m.secretInformer.Informer().GetStore().GetByKey(secretKey)
	if !exists {
		return nil, nil, fmt.Errorf("Notification Secret does not exist")
	}
	if err != nil {
		return nil, nil, err
	}

	templates := map[string][]string{}

	// Creates config for notifications
	cfg, err := pkg.ParseConfig(configMap.(*v1.ConfigMap), secret.(*v1.Secret))
	for name, triggers := range cfg.Triggers {
		if _, ok := TriggerToCondition[name]; ok { // match event names from RO
			templates[name] = triggers[0].Send
			delete(cfg.Triggers, name)
		}

	}
	if err != nil {
		return nil, nil, err
	}

	api, err := pkg.NewAPI(*cfg)

	// Cache later
	return api, templates, err
}
