package server

import (
	"io"
	"sync"
	"testing"
	"time"
)

func TestLogSinkConcurrentPublishAndCancel(t *testing.T) {
	s := &logSinkT{cap: 8, subs: map[chan LogLine]struct{}{}, out: io.Discard}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 1000; j++ {
				s.Write([]byte("published\n"))
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 1000; j++ {
				ch, cancel := s.subscribe()
				cancel()
				cancel()
				for range ch {
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(s.subs) != 0 {
		t.Fatal("cancelled subscribers remain registered")
	}
}

func TestLogSinkFullSubscriberDoesNotBlock(t *testing.T) {
	s := &logSinkT{cap: 8, subs: map[chan LogLine]struct{}{}, out: io.Discard}
	ch, cancel := s.subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.Write([]byte("published\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("full subscriber blocked publication")
	}
	if len(ch) != 256 {
		t.Fatalf("buffered lines = %d, want 256", len(ch))
	}
	lines, cursor := s.recent(0, 8)
	if cursor != 1000 || len(lines) != 8 {
		t.Fatalf("ring cursor=%d lines=%d", cursor, len(lines))
	}
}
