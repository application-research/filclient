package rep

import "sync"

type RetrievalEventPublisher struct {
	// Lock for the subscribers list
	subscribersLk sync.RWMutex
	// The list of subscribers
	subscriberList []RetrievalSubscriber
}

// A return function unsubscribing a subscribed Subscriber via the Subscribe() function
type UnsubscribeFn func()

func New() *RetrievalEventPublisher {
	return &RetrievalEventPublisher{subscriberList: make([]RetrievalSubscriber, 0)}
}

func (ep *RetrievalEventPublisher) Subscribe(subscriber RetrievalSubscriber) UnsubscribeFn {
	// Lock writes on the subscribers list
	ep.subscribersLk.Lock()
	defer ep.subscribersLk.Unlock()

	// Don't add the subscriber if they're already in the list
	for _, subInList := range ep.subscriberList {
		if subInList.RetrievalSubscriberId() == subscriber.RetrievalSubscriberId() {
			return ep.unsubscribeWith(subscriber) // Just return the unsubscribe function
		}
	}

	// Add the subscriber to the list
	ep.subscriberList = append(ep.subscriberList, subscriber)

	// Return the unsubscribe function
	return ep.unsubscribeWith(subscriber)
}

func (ep *RetrievalEventPublisher) Publish(event RetrievalEvent, state RetrievalState) {
	go func() {
		// Lock reads on the subscriber list
		ep.subscribersLk.RLock()
		defer ep.subscribersLk.RUnlock()

		// Execute the subscriber function for each subscriber
		for _, subscriber := range ep.subscriberList {
			subscriber.OnRetrievalEvent(event, state)
		}
	}()
}

// Returns the number of retrieval event subscribers
func (ep *RetrievalEventPublisher) SubscriberCount() int {
	ep.subscribersLk.RLock()
	defer ep.subscribersLk.RUnlock()
	return len(ep.subscriberList)
}

func (ep *RetrievalEventPublisher) unsubscribeWith(subscriber RetrievalSubscriber) UnsubscribeFn {
	return func() {
		// Lock writes on the subscribers list
		ep.subscribersLk.Lock()
		defer ep.subscribersLk.Unlock()

		curLen := len(ep.subscriberList)
		for i, subInList := range ep.subscriberList {
			// Remove the subscriber if they're in the list
			if subInList.RetrievalSubscriberId() == subscriber.RetrievalSubscriberId() {
				ep.subscriberList[i] = ep.subscriberList[curLen-1]
				ep.subscriberList = ep.subscriberList[:curLen-1]
			}
		}
	}
}
