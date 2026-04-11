package dispatch

// Queue represents a position in the 4-queue assembly line.
type Queue int

const (
	QueueGroom      Queue = iota // Q0: strategy → issues
	QueueIntake                  // Q1: issue → planned
	QueueBuild                   // Q2: planned → implemented
	QueueValidate                // Q3: implemented → validated
	QueueDone                    // Terminal: validated
	QueueHuman                   // Terminal: needs-human
	QueueInProgress              // Transient: agent:claimed
)

var queuePriority = []Queue{QueueValidate, QueueBuild, QueueIntake, QueueGroom}

// QueueMachine manages the label-driven state machine for the assembly line.
type QueueMachine struct {
	GroomThreshold int
}

func NewQueueMachine() *QueueMachine {
	return &QueueMachine{GroomThreshold: 5}
}

func (qm *QueueMachine) ClassifyQueue(labels []string) Queue {
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		set[l] = true
	}
	if set["needs-human"] {
		return QueueHuman
	}
	if set["validated"] {
		return QueueDone
	}
	if set["agent:claimed"] {
		return QueueInProgress
	}
	if set["needs-fix"] {
		return QueueBuild
	}
	if set["implemented"] {
		return QueueValidate
	}
	if set["planned"] {
		return QueueBuild
	}
	return QueueIntake
}

func (qm *QueueMachine) NextLabel(queue Queue, success bool) string {
	switch queue {
	case QueueIntake:
		return "planned"
	case QueueBuild:
		return "implemented"
	case QueueValidate:
		if success {
			return "validated"
		}
		return "needs-fix"
	default:
		return ""
	}
}

func (qm *QueueMachine) PickHighestPriority(counts map[Queue]int) Queue {
	for _, q := range queuePriority {
		if counts[q] > 0 {
			return q
		}
	}
	return QueueGroom
}

func (qm *QueueMachine) NeedsGroom(intakeCount int) bool {
	return intakeCount < qm.GroomThreshold
}

func (qm *QueueMachine) ComplexityFromLabels(labels []string) string {
	for _, l := range labels {
		switch l {
		case "complexity:low":
			return "low"
		case "complexity:med":
			return "med"
		case "complexity:high":
			return "high"
		}
	}
	return "low"
}
