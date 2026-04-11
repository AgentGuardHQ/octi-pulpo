package dispatch

import "testing"

func TestQueueMachine_ClassifyQueue(t *testing.T) {
	qm := NewQueueMachine()
	tests := []struct {
		labels []string
		want   Queue
	}{
		{[]string{"tier:b-scope"}, QueueIntake},
		{[]string{"tier:b-scope", "planned"}, QueueBuild},
		{[]string{"tier:b-scope", "implemented"}, QueueValidate},
		{[]string{"tier:b-scope", "validated"}, QueueDone},
		{[]string{"tier:b-scope", "needs-fix"}, QueueBuild},
		{[]string{"tier:b-scope", "needs-human"}, QueueHuman},
		{[]string{"tier:b-scope", "agent:claimed"}, QueueInProgress},
	}
	for _, tt := range tests {
		got := qm.ClassifyQueue(tt.labels)
		if got != tt.want {
			t.Errorf("ClassifyQueue(%v) = %v, want %v", tt.labels, got, tt.want)
		}
	}
}

func TestQueueMachine_NextLabel(t *testing.T) {
	qm := NewQueueMachine()
	tests := []struct {
		queue     Queue
		success   bool
		wantLabel string
	}{
		{QueueIntake, true, "planned"},
		{QueueBuild, true, "implemented"},
		{QueueValidate, true, "validated"},
		{QueueValidate, false, "needs-fix"},
	}
	for _, tt := range tests {
		got := qm.NextLabel(tt.queue, tt.success)
		if got != tt.wantLabel {
			t.Errorf("NextLabel(%v, %v) = %q, want %q", tt.queue, tt.success, got, tt.wantLabel)
		}
	}
}

func TestQueueMachine_PickQueue(t *testing.T) {
	qm := NewQueueMachine()
	counts := map[Queue]int{
		QueueIntake:   3,
		QueueBuild:    2,
		QueueValidate: 1,
	}
	got := qm.PickHighestPriority(counts)
	if got != QueueValidate {
		t.Errorf("PickHighestPriority = %v, want QueueValidate", got)
	}
	delete(counts, QueueValidate)
	got = qm.PickHighestPriority(counts)
	if got != QueueBuild {
		t.Errorf("PickHighestPriority = %v, want QueueBuild", got)
	}
}

func TestQueueMachine_GroomTrigger(t *testing.T) {
	qm := NewQueueMachine()
	qm.GroomThreshold = 5
	if qm.NeedsGroom(6) {
		t.Error("NeedsGroom(6) should be false with threshold 5")
	}
	if !qm.NeedsGroom(3) {
		t.Error("NeedsGroom(3) should be true with threshold 5")
	}
}

func TestQueueMachine_ComplexityFromLabels(t *testing.T) {
	qm := NewQueueMachine()
	tests := []struct {
		labels []string
		want   string
	}{
		{[]string{"complexity:low"}, "low"},
		{[]string{"complexity:med"}, "med"},
		{[]string{"complexity:high", "sprint"}, "high"},
		{[]string{"sprint"}, "low"},
	}
	for _, tt := range tests {
		got := qm.ComplexityFromLabels(tt.labels)
		if got != tt.want {
			t.Errorf("ComplexityFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
		}
	}
}
