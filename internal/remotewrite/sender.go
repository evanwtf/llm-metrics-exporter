package remotewrite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/prompb"
)

const maxBatch = 4 << 20

type Options struct {
	URL, Dir, Host, TokenFile, Version string
	Interval, Timeout, MaxAge          time.Duration
	MaxBytes                           int64
}

func (o Options) Validate() error {
	u, err := url.Parse(o.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("remote-write URL must be an http(s) URL without credentials, query, or fragment")
	}
	if o.Dir == "" || o.Host == "" || o.Interval < time.Millisecond || o.Timeout <= 0 || o.MaxAge <= 0 || o.MaxBytes < maxBatch {
		return errors.New("remote-write requires directory, stable host, positive timeout/retention, interval >=1ms, and queue >=4MiB")
	}
	return nil
}

// Status is exposed as JSON instead of unlabeled exporter-global metrics.
type Status struct {
	QueuedBytes    int64  `json:"queued_bytes"`
	QueuedBatches  int    `json:"queued_batches"`
	DroppedBatches uint64 `json:"dropped_batches"`
	LastSuccess    int64  `json:"last_success_unix_seconds"`
	LastError      string `json:"last_error"`
}

type Sender struct {
	opts      Options
	gather    prometheus.Gatherer
	log       *slog.Logger
	client    *http.Client
	lock      *os.File
	mu        sync.Mutex
	files     []string
	status    Status
	previous  []prompb.TimeSeries
	timestamp int64
	wake      chan struct{}
}

func New(o Options, g prometheus.Gatherer, log *slog.Logger) (*Sender, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(o.Dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(o.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("remote-write queue already in use")
	}
	s := &Sender{opts: o, gather: g, log: log, lock: lock, wake: make(chan struct{}, 1), client: &http.Client{Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err = s.restore(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Sender) Close() { _ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN); _ = s.lock.Close() }

func atomicWrite(dir, name string, body []byte) error {
	f, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Sender) restore() error {
	// Bind queue history to a destination and stable identity.
	sum := sha256.Sum256([]byte(s.opts.URL + "\x00" + s.opts.Host))
	want := hex.EncodeToString(sum[:])
	manifest := filepath.Join(s.opts.Dir, "destination")
	body, err := os.ReadFile(manifest)
	if err == nil && string(body) != want {
		return errors.New("remote-write queue belongs to another destination or host; use a different directory")
	}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = atomicWrite(s.opts.Dir, "destination", []byte(want)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(s.opts.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".pending-") {
			if err = os.Remove(filepath.Join(s.opts.Dir, name)); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(name, ".rw") {
			continue
		}
		if _, err = strconv.ParseInt(strings.TrimSuffix(name, ".rw"), 10, 64); err != nil {
			return errors.New("invalid remote-write queue filename")
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxBatch {
			return errors.New("invalid remote-write queue file")
		}
		s.files = append(s.files, name)
		s.status.QueuedBytes += info.Size()
	}
	sort.Strings(s.files)
	s.status.QueuedBatches = len(s.files)
	if s.status.QueuedBytes > s.opts.MaxBytes {
		return errors.New("existing remote-write queue exceeds configured cap; restore the previous cap to drain it")
	}
	// The last accepted snapshot survives draining the queue. If a crash
	// happened between enqueue and checkpoint, the newest queue file wins.
	candidates := []string{"state"}
	if len(s.files) > 0 {
		candidates = append(candidates, s.files[len(s.files)-1])
	}
	for _, name := range candidates {
		path := filepath.Join(s.opts.Dir, name)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Size() > maxBatch {
			return errors.New("remote-write checkpoint too large")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		decodedSize, err := snappy.DecodedLen(body)
		if err != nil || decodedSize > maxBatch {
			return errors.New("invalid remote-write checkpoint compression")
		}
		raw, err := snappy.Decode(nil, body)
		if err != nil {
			return err
		}
		var request prompb.WriteRequest
		if err = request.Unmarshal(raw); err != nil {
			return err
		}
		var ts int64
		for _, series := range request.Timeseries {
			for _, sample := range series.Samples {
				ts = max(ts, sample.Timestamp)
			}
		}
		if ts >= s.timestamp {
			s.timestamp = ts
			s.previous = activeSeries(&request)
		}
	}
	return nil
}

func (s *Sender) Status() Status { s.mu.Lock(); defer s.mu.Unlock(); return s.status }
func (s *Sender) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Status())
}

func (s *Sender) enqueue(current []prompb.TimeSeries, ts int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	request := prompb.WriteRequest{Timeseries: withStale(current, s.previous, ts)}
	if len(request.Timeseries) == 0 {
		return nil
	}
	raw, err := request.Marshal()
	if err != nil {
		return err
	}
	if len(raw) > maxBatch {
		s.status.DroppedBatches++
		return errors.New("remote-write snapshot exceeds 4MiB")
	}
	body := snappy.Encode(nil, raw)
	if len(body) > maxBatch || s.status.QueuedBytes+int64(len(body)) > s.opts.MaxBytes {
		s.status.DroppedBatches++
		return errors.New("remote-write queue full; dropping newest snapshot")
	}
	name := fmt.Sprintf("%020d.rw", ts)
	if err = atomicWrite(s.opts.Dir, name, body); err != nil {
		return err
	}
	s.files = append(s.files, name)
	s.status.QueuedBytes += int64(len(body))
	s.status.QueuedBatches = len(s.files)
	s.previous = current
	s.timestamp = ts
	if err = atomicWrite(s.opts.Dir, "state", body); err != nil {
		return err
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *Sender) collect() error {
	families, err := s.gather.Gather()
	if err != nil {
		return err
	}
	s.mu.Lock()
	ts := max(time.Now().UnixMilli(), s.timestamp+1)
	s.mu.Unlock()
	current, err := encodeFamilies(families, s.opts.Host, ts)
	if err != nil {
		return err
	}
	return s.enqueue(current, ts)
}

func (s *Sender) report(err error) {
	s.mu.Lock()
	s.status.LastError = err.Error()
	s.mu.Unlock()
	s.log.Warn("remote write", "err", err)
}

// sendOne preserves FIFO order and retries the identical payload. A lost ACK
// can cause an identical duplicate; timestamps and values are not rewritten.
func (s *Sender) sendOne(ctx context.Context) (bool, error) {
	s.mu.Lock()
	if len(s.files) == 0 {
		s.mu.Unlock()
		return false, nil
	}
	name := s.files[0]
	s.mu.Unlock()
	body, err := os.ReadFile(filepath.Join(s.opts.Dir, name))
	if err != nil {
		return false, err
	}
	ts, _ := strconv.ParseInt(strings.TrimSuffix(name, ".rw"), 10, 64)
	expired := time.Since(time.UnixMilli(ts)) > s.opts.MaxAge
	rejected := false
	if !expired {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.opts.URL, bytes.NewReader(body))
		if err != nil {
			return false, errors.New("cannot create remote-write request")
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Content-Encoding", "snappy")
		req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
		req.Header.Set("User-Agent", "llm-metrics-exporter/"+s.opts.Version)
		if s.opts.TokenFile != "" {
			file, err := os.Open(s.opts.TokenFile)
			if err != nil {
				return false, errors.New("cannot read remote-write token file")
			}
			token, err := io.ReadAll(io.LimitReader(file, 16385))
			file.Close()
			if err != nil || len(token) > 16384 || strings.TrimSpace(string(token)) == "" {
				return false, errors.New("remote-write token must be nonempty and at most 16KiB")
			}
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return false, errors.New("remote-write transport failed (check connectivity, TLS, and timeout)")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("remote-write retryable HTTP %d", resp.StatusCode)
		}
		rejected = resp.StatusCode < 200 || resp.StatusCode >= 300
		if rejected {
			s.report(fmt.Errorf("remote-write rejected HTTP %d; dropping batch", resp.StatusCode))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = os.Remove(filepath.Join(s.opts.Dir, name)); err != nil {
		return false, err
	}
	s.files = s.files[1:]
	s.status.QueuedBytes -= int64(len(body))
	s.status.QueuedBatches = len(s.files)
	if expired || rejected {
		s.status.DroppedBatches++
	} else {
		s.status.LastSuccess = time.Now().Unix()
		s.status.LastError = ""
	}
	if expired {
		s.status.LastError = "remote-write batch expired; data dropped"
		s.log.Warn(s.status.LastError)
	}
	dir, err := os.Open(s.opts.Dir)
	if err != nil {
		return true, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Sender) deliver(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		sent, err := s.sendOne(ctx)
		if err == nil && sent {
			delay = time.Second
			continue
		}
		if err != nil {
			s.report(err)
		}
		timer := time.NewTimer(delay)
		if err != nil {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(delay*2, time.Minute)
		} else {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.wake:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

// Run has one collection loop and one delivery loop. Shutdown checkpoints
// staleness to disk, then makes one bounded delivery attempt; remaining data
// replays on restart. It never waits indefinitely for a receiver.
func (s *Sender) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() { defer close(done); s.deliver(ctx) }()
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := s.collect(); err != nil {
			s.report(err)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
			continue
		}
		break
	}
	<-done
	s.mu.Lock()
	ts := max(time.Now().UnixMilli(), s.timestamp+1)
	s.mu.Unlock()
	if err := s.enqueue(nil, ts); err != nil {
		s.report(err)
	}
	flush, cancel := context.WithTimeout(context.Background(), s.opts.Timeout)
	defer cancel()
	if _, err := s.sendOne(flush); err != nil {
		s.report(err)
	}
}
