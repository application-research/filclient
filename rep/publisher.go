package rep

import "sync"

type RetrievalEventPublisher struct {
	mu          sync.Mutex
	subscribers map[RetrievalSubscriber]struct{}
}

func NewEventPublisher() (*RetrievalEventPublisher, error) {
	return &RetrievalEventPublisher{
		subscribers: map[RetrievalSubscriber]struct{}{},
	}, nil
}

func (ep *RetrievalEventPublisher) SubscribeToRetrievalEvents(subscriber RetrievalSubscriber) {
	// mutex lock writes
	ep.mu.Lock()
	defer ep.mu.Unlock()
	ep.subscribers[subscriber] = struct{}{}
}

func (ep *RetrievalEventPublisher) UnsubscribeFromRetrievalEvents(subscriber RetrievalSubscriber) {
	// mutex lock writes
	ep.mu.Lock()
	defer ep.mu.Unlock()
	delete(ep.subscribers, subscriber)
}

func (ep *RetrievalEventPublisher) PublishRetrievalEvent(event RetrievalEvent) {
	go func() {
		for subscriber := range ep.subscribers {
			subscriber.OnRetrievalEvent(event)
		}
	}()
}
