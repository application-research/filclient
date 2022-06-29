package rep

type OnRetrievalEvent func()

type RetrievalSubscriber interface {
	OnRetrievalEvent(RetrievalEvent, RetrievalState)
	// RetrievalSubscriberId must return a unique identifier of a comparable type for this subscriber
	RetrievalSubscriberId() interface{}
}
