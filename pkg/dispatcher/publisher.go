package dispatcher

type Publisher interface {
	Publish(subject string, data []byte) error
}

type MockPublisher struct {
	fn func(subj string, data []byte)
}

func NewMockPublisher(fn func(subj string, data []byte)) *MockPublisher {
	return &MockPublisher{fn: fn}
}

func NewFuncPublisher(fn func(subj string, data []byte)) *MockPublisher {
	return &MockPublisher{fn: fn}
}

func (p *MockPublisher) Publish(subject string, data []byte) error {
	if p.fn != nil {
		p.fn(subject, data)
	}
	return nil
}
