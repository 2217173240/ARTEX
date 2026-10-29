package server

import (
	"sync"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestBroadcasterOverflowClosesBeforeLaterFrames(t *testing.T) {
	b := NewBroadcaster()
	ch, unsub := b.Subscribe("slow")
	fast, fastUnsub := b.Subscribe("slow")
	defer fastUnsub()
	other, otherUnsub := b.Subscribe("other")
	defer otherUnsub()
	for id := int64(1); id <= 257; id++ {
		b.Publish("slow", db.Activity{ID: id})
		if a := <-fast; a.ID != id {
			t.Fatalf("healthy subscriber ID=%d want=%d", a.ID, id)
		}
	}
	if a := <-ch; a.ID != 1 {
		t.Fatalf("first ID=%d", a.ID)
	}
	b.Publish("slow", db.Activity{ID: 258})
	if a := <-fast; a.ID != 258 {
		t.Fatal(a.ID)
	}
	want := int64(2)
	for a := range ch {
		if a.ID != want {
			t.Fatalf("ID=%d, want %d", a.ID, want)
		}
		want++
	}
	if want != 257 {
		t.Fatalf("prefix ended at %d", want-1)
	}
	unsub()
	unsub()
	fastUnsub()
	b.Publish("other", db.Activity{ID: 259})
	if a := <-other; a.ID != 259 {
		t.Fatal(a.ID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.subs["slow"]; exists {
		t.Fatal("overflow subscription retained")
	}
}

func TestBroadcasterConcurrentUnsubscribeAndOverflow(t *testing.T) {
	b := NewBroadcaster()
	for iteration := 0; iteration < 100; iteration++ {
		_, unsub := b.Subscribe("task")
		var wg sync.WaitGroup
		for producer := 0; producer < 4; producer++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for id := 0; id < 300; id++ {
					b.Publish("task", db.Activity{ID: int64(id)})
				}
			}()
		}
		wg.Add(1)
		go func() { defer wg.Done(); unsub(); unsub() }()
		wg.Wait()
		unsub()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) != 0 {
		t.Fatalf("retained subscriptions: %d", len(b.subs))
	}
}
