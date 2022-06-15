package rep

type OnRetrievalEvent func(RetrievalEvent)

type RetrievalSubscriber interface {
	OnRetrievalEvent(RetrievalEvent)
}
