package rep

type RetrievalSubscriber interface {
	OnRetrievalEvent(RetrievalEvent)
}
