package notifications

import (
	"github.com/argoproj/notifications-engine/pkg"
	kubeinformers "k8s.io/client-go/informers"
)

type NotificationsManager interface {
	GetAPI() (pkg.API, error)
}

func NewNotificationsManager(informersFactory kubeinformers.SharedInformerFactory) *notificationsManager {
	return nil
}

type notificationsManager struct {
	api              pkg.API
	informersFactory kubeinformers.SharedInformerFactory
}

func (m *notificationsManager) GetAPI() (pkg.API, error) {
	//if m.api == nil {
	//	// init everything
	//	m.informersFactory.Core().V1().ConfigMaps()
	//}
	panic("Not implemented")
	return nil, nil
}
