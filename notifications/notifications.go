package notifications

import (
	"github.com/argoproj/notifications-engine/pkg"
	v1 "k8s.io/api/core/v1"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
)

type NotificationsManager interface {
	GetAPI() (pkg.API, map[string][]string, error)
}

func NewNotificationsManager(informersFactory kubeinformers.SharedInformerFactory) *notificationsManager {
	return nil
}

type notificationsManager struct {
	api              pkg.API
	informersFactory kubeinformers.SharedInformerFactory
	kubeclientset    kubernetes.Clientset
}

func (m *notificationsManager) GetAPI() (pkg.API, map[string][]string, error) {
	if m.api == nil {
		// init everything
		// Create in main.go
		//cmInformer := m.informersFactory.Core().V1().ConfigMaps().Informer()
		//secretInformer := m.informersFactory.Core().V1().Secrets()
		// Add event handlers

		// "argo-rollout-notification-configmap"
		// "argo-rollout-notification-secret"
	}

	templates := map[string][]string{}

	var configMap *v1.ConfigMap
	var secret *v1.Secret

	// Creates config for notifications
	cfg, err := pkg.ParseConfig(configMap, secret)
	for name, conditions := range cfg.Triggers {
		if name == "built-in" { // match event names from RO
			templates[name] = conditions[0].Send
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

