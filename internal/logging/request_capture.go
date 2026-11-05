package logging

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// CaptureRecord v1 identifies an inbound exchange, independently of usage executions.
// HTTP bodies are decoded in the worker; frames are bounded downstream messages.
type CaptureRecord struct {
	ID               string           `json:"id"`
	TraceID          string           `json:"traceId"`
	Epoch            string           `json:"epoch"`
	Sequence         uint64           `json:"sequence"`
	StartedAt        string           `json:"startedAt"`
	Method           string           `json:"method"`
	Path             string           `json:"path"`
	Status           int              `json:"status"`
	State            string           `json:"state"`
	Reason           string           `json:"reason"`
	Request          string           `json:"request"`
	Response         string           `json:"response"`
	RequestType      string           `json:"requestType"`
	ResponseType     string           `json:"responseType"`
	RequestEncoding  string           `json:"requestEncoding"`
	ResponseEncoding string           `json:"responseEncoding"`
	Format           string           `json:"format"`
	RequestDecoded   bool             `json:"requestDecoded"`
	ResponseDecoded  bool             `json:"responseDecoded"`
	Frames           []CaptureFrame   `json:"frames,omitempty"`
	RawRequest       []byte           `json:"-"`
	RawResponse      []byte           `json:"-"`
	Messages         *CaptureMessages `json:"-"`
}

const CaptureBodyLimit = 256 << 10
const captureBudget = 16 << 20
const captureRecordLimit = 256

type captureEnvelope struct {
	generation uint64
	record     CaptureRecord
}

// CaptureSpool does no disk I/O on inference. A bounded finalization queue can lose
// records; counters and sequences expose this. Restart loses the entire memory spool.
type CaptureSpool struct {
	mu         sync.Mutex
	epoch      string
	generation uint64
	sequence   uint64
	dropped    uint64
	until      time.Time
	records    []CaptureRecord
	bytes      int
	queue      chan captureEnvelope
	active     chan struct{}
	wsActive   chan struct{}
}

func NewCaptureSpool() *CaptureSpool {
	s := &CaptureSpool{epoch: uuid.NewString(), queue: make(chan captureEnvelope, 8), active: make(chan struct{}, 16), wsActive: make(chan struct{}, 16)}
	go func() {
		for e := range s.queue {
			s.store(e)
		}
	}()
	return s
}

var DesktopCaptures = NewCaptureSpool()

// Lease is operational, not config: opt-in expires after 60 seconds without renewal.
func (s *CaptureSpool) Lease(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled {
		s.until = time.Now().Add(time.Minute)
		return
	}
	s.until = time.Time{}
	s.generation++
	s.records = nil
	s.bytes = 0
}

func (s *CaptureSpool) Begin() (uint64, bool) {
	return s.begin(s.active)
}
func (s *CaptureSpool) BeginWebsocket() (uint64, bool) { return s.begin(s.wsActive) }
func (s *CaptureSpool) begin(permits chan struct{}) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !time.Now().Before(s.until) {
		return 0, false
	}
	select {
	case permits <- struct{}{}:
		return s.generation, true
	default:
		s.dropped++
		return 0, false
	}
}

func (s *CaptureSpool) Finish(generation uint64, r CaptureRecord) {
	defer func() { <-s.active }()
	s.Update(generation, r)
}
func (s *CaptureSpool) FinishWebsocket(generation uint64, r CaptureRecord) {
	defer func() { <-s.wsActive }()
	s.Update(generation, r)
}

// Update publishes a best-effort revision without releasing a live connection permit.
func (s *CaptureSpool) Update(generation uint64, r CaptureRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return
	}
	if !time.Now().Before(s.until) {
		s.dropped++
		return
	}
	s.sequence++
	r.Sequence, r.Epoch = s.sequence, s.epoch
	select {
	case s.queue <- captureEnvelope{generation, r}:
	default:
		s.dropped++
	}
}

func (s *CaptureSpool) store(e captureEnvelope) {
	e.record = NormalizeCapture(e.record)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation != s.generation {
		return
	}
	if !time.Now().Before(s.until) {
		s.dropped++
		return
	}
	size := captureRecordSize(e.record)
	for len(s.records) > 0 && (s.bytes+size > captureBudget || len(s.records) >= captureRecordLimit) {
		s.bytes -= captureRecordSize(s.records[0])
		s.records = s.records[1:]
		s.dropped++
	}
	s.records = append(s.records, e.record)
	s.bytes += size
}

func captureRecordSize(r CaptureRecord) int {
	size := len(r.Request) + len(r.Response)
	for _, f := range r.Frames {
		size += len(f.Payload) + 128
	}
	return size
}

type CapturePage struct {
	Version int             `json:"version"`
	Epoch   string          `json:"epoch"`
	Dropped uint64          `json:"dropped"`
	Records []CaptureRecord `json:"records"`
}

// Page returns at most four bounded records; clients persist before advancing cursor.
func (s *CaptureSpool) Page(after uint64) CapturePage {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := CapturePage{Version: 1, Epoch: s.epoch, Dropped: s.dropped, Records: []CaptureRecord{}}
	if !time.Now().Before(s.until) {
		s.records = nil
		s.bytes = 0
		return p
	}
	for _, r := range s.records {
		if r.Sequence > after {
			p.Records = append(p.Records, r)
			if len(p.Records) == 4 {
				break
			}
		}
	}
	return p
}
