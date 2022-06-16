package rep

type OnRetrievalEvent func(RetrievalEvent)

type RetrievalSubscriber interface {
	OnRetrievalEvent(RetrievalEvent)
	// SubscriberIdentifier must return a comparable identifier for this subscriber
	SubscriberIdentifier() interface{}
}
