package rep

import "sync"

type RetrievalEventPublisher struct {
	subscribersLk  sync.RWMutex
	subscriberList []RetrievalSubscriber
}

func New() *RetrievalEventPublisher {
	return &RetrievalEventPublisher{subscriberList: make([]RetrievalSubscriber, 0)}
}

func (ep *RetrievalEventPublisher) Subscribe(subscriber RetrievalSubscriber) {
	// Lock writes on the subscribers list
	ep.subscribersLk.Lock()
	defer ep.subscribersLk.Unlock()

	for _, subInList := range ep.subscriberList {
		// Don't add the subscriber again if they're already in the list
		if subInList == subscriber {
			return
		}
	}

	// Add the subscriber to the list
	ep.subscriberList = append(ep.subscriberList, subscriber)
}

func (ep *RetrievalEventPublisher) Unsubscribe(subscriber RetrievalSubscriber) {
	// Lock writes on the subscribers list
	ep.subscribersLk.Lock()
	defer ep.subscribersLk.Unlock()

	curLen := len(ep.subscriberList)
	for i, subInList := range ep.subscriberList {
		// Remove the subscriber if they're in the list
		if subInList == subscriber {
			ep.subscriberList[i] = ep.subscriberList[curLen-1]
			ep.subscriberList = ep.subscriberList[:curLen-1]
			return
		}
	}
}

func (ep *RetrievalEventPublisher) Publish(event RetrievalEvent) {
	go func() {
		// Lock reads on the subscriber list
		ep.subscribersLk.RLock()
		defer ep.subscribersLk.RUnlock()

		// Execute the subscriber function for each subscriber
		for _, subscriber := range ep.subscriberList {
			subscriber.OnRetrievalEvent(event)
		}
	}()
}
