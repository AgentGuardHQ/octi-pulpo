package dispatch

import (
	"testing"
	"time"
)

func TestSkipList_InMemory(t *testing.T) {
	sl := NewSkipList(nil, "test")

	if sl.IsSkipped("octi#119") {
		t.Error("expected not skipped initially")
	}

	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	if sl.IsSkipped("octi#119") {
		t.Error("expected not skipped after 2 rejections")
	}

	sl.RecordRejection("octi#119")
	if !sl.IsSkipped("octi#119") {
		t.Error("expected skipped after 3 rejections")
	}

	sl.Clear("octi#119")
	if sl.IsSkipped("octi#119") {
		t.Error("expected not skipped after clear")
	}
}

func TestSkipList_Expiry(t *testing.T) {
	sl := NewSkipList(nil, "test")
	sl.TTL = 1 * time.Millisecond

	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")

	if !sl.IsSkipped("octi#119") {
		t.Fatal("expected skipped")
	}

	time.Sleep(5 * time.Millisecond)
	sl.ExpireOld()

	if sl.IsSkipped("octi#119") {
		t.Error("expected expired after TTL")
	}
}

func TestSkipList_ListAll(t *testing.T) {
	sl := NewSkipList(nil, "test")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("chitin#5")
	sl.RecordRejection("chitin#5")
	sl.RecordRejection("chitin#5")

	all := sl.ListAll()
	if len(all) != 2 {
		t.Errorf("expected 2 skipped, got %d", len(all))
	}
}
