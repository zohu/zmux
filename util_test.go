package zmux

import "testing"

func TestSendNotify(t *testing.T) {
	ch := make(chan struct{})
	sendNotify(ch)
	select {
	case <-ch:
		t.Fatalf("should not get")
	default:
	}

	ch = make(chan struct{}, 1)
	sendNotify(ch)
	select {
	case <-ch:
	default:
		t.Fatalf("should get")
	}
}

func TestSendErr(t *testing.T) {
	ch := make(chan error)
	sendErr(ch, ErrTimeout)
	select {
	case <-ch:
		t.Fatalf("should not get")
	default:
	}

	ch = make(chan error, 1)
	sendErr(ch, ErrTimeout)
	select {
	case <-ch:
	default:
		t.Fatalf("should get")
	}
}
func TestLogger(t *testing.T) {
	logger := defaultLogger{}
	logger.Infof("hello %v", "world")
	logger.Errorf("hello %v", "world")
}
