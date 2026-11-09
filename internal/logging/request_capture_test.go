package logging

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCaptureRecordAlwaysSendsSideCuts(t *testing.T) {
	data, err := json.Marshal(CaptureRecord{Frames: []CaptureFrame{{Payload: "complete"}}})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); !strings.Contains(s, `"requestCut":null`) || !strings.Contains(s, `"responseCut":null`) || strings.Contains(s, `"cut"`) {
		t.Fatalf("complete sides must send null cuts and complete frames no cut: %s", s)
	}
}

func testCaptureSpool() *CaptureSpool {
	return &CaptureSpool{epoch: "test-epoch", queue: make(chan captureEnvelope, 8), active: make(chan struct{}, 16), wsActive: make(chan struct{}, 16)}
}

func TestCaptureSpoolLeaseOverflowReplayAndDisable(t *testing.T) {
	s := testCaptureSpool()
	if _, ok := s.Begin(); ok {
		t.Fatal("capture enabled by default")
	}
	s.Lease(true)
	for i := 0; i < 10; i++ {
		g, ok := s.Begin()
		if !ok {
			t.Fatal("begin failed")
		}
		s.Finish(g, CaptureRecord{ID: fmt.Sprint(i)})
	}
	if s.dropped != 2 {
		t.Fatalf("dropped=%d", s.dropped)
	}
	for len(s.queue) > 0 {
		s.store(<-s.queue)
	}
	p := s.Page(0)
	if len(p.Records) != 4 || p.Records[0].Sequence != 1 || p.Epoch != "test-epoch" {
		t.Fatalf("bad page: %+v", p)
	}
	if len(s.Page(4).Records) != 4 || len(s.Page(8).Records) != 0 {
		t.Fatal("cursor paging failed")
	}
	if s.Page(0).Records[0].ID != p.Records[0].ID {
		t.Fatal("replay changed identity")
	}
	g, _ := s.Begin()
	s.Lease(false)
	s.Finish(g, CaptureRecord{ID: "stale"})
	s.Lease(true)
	if len(s.queue) > 0 {
		s.store(<-s.queue)
	}
	if len(s.Page(0).Records) != 0 {
		t.Fatal("old generation retained after disable")
	}
	s.until = time.Now().Add(-time.Second)
	if _, ok := s.Begin(); ok {
		t.Fatal("expired lease captured")
	}
}

func TestCaptureSpoolBoundsActiveRequestsAndRetention(t *testing.T) {
	s := testCaptureSpool()
	s.Lease(true)
	for i := 0; i < 16; i++ {
		if _, ok := s.Begin(); !ok {
			t.Fatal("active permit unexpectedly refused")
		}
	}
	if _, ok := s.Begin(); ok {
		t.Fatal("active limit exceeded")
	}
	for i := 0; i < 40; i++ {
		s.store(captureEnvelope{record: CaptureRecord{Sequence: uint64(i + 1), Request: strings.Repeat("x", CaptureBodyLimit), Response: strings.Repeat("y", CaptureBodyLimit)}})
	}
	if s.bytes > captureBudget || len(s.records) > captureRecordLimit || s.records[0].Sequence != uint64(41-captureBudget/(2*CaptureBodyLimit)) {
		t.Fatalf("budget not enforced: %d bytes, first %d", s.bytes, s.records[0].Sequence)
	}
	s.until = time.Now().Add(-time.Second)
	if len(s.Page(0).Records) != 0 || s.bytes != 0 {
		t.Fatal("expired memory spool not cleared")
	}
}
