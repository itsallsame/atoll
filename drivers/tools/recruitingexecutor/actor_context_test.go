package recruitingexecutor

import (
	"context"
	"testing"
	"time"
)

func TestBoundedExecutionContextOutlivesWakeButKeepsHardLimit(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	execution, cancelExecution := boundedExecutionContext(parent)
	defer cancelExecution()
	cancelParent()

	select {
	case <-execution.Done():
		t.Fatal("durable execution was canceled with its Wake delivery")
	default:
	}
	deadline, ok := execution.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= maxExecutionLifetime-time.Minute || remaining > maxExecutionLifetime {
		t.Fatalf("execution deadline remaining=%v ok=%v", remaining, ok)
	}
}
