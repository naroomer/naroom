package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cityRowsScript struct {
	values [][]driver.Value
	errAt  int
	err    error
}

type scriptedCityDriver struct {
	mu      sync.Mutex
	scripts []cityRowsScript
	queries int
}

func (d *scriptedCityDriver) Open(string) (driver.Conn, error) {
	return &scriptedCityConn{driver: d}, nil
}

type scriptedCityConn struct {
	driver *scriptedCityDriver
}

func (c *scriptedCityConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (c *scriptedCityConn) Close() error              { return nil }
func (c *scriptedCityConn) Begin() (driver.Tx, error) { return nil, errors.New("begin not supported") }
func (c *scriptedCityConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	c.driver.queries++
	if len(c.driver.scripts) == 0 {
		return nil, errors.New("unexpected query")
	}
	script := c.driver.scripts[0]
	c.driver.scripts = c.driver.scripts[1:]
	return &scriptedCityRows{script: script}, nil
}

var _ driver.QueryerContext = (*scriptedCityConn)(nil)

type scriptedCityRows struct {
	script cityRowsScript
	index  int
}

func (r *scriptedCityRows) Columns() []string { return []string{"city", "cnt"} }
func (r *scriptedCityRows) Close() error      { return nil }
func (r *scriptedCityRows) Next(dest []driver.Value) error {
	if r.script.err != nil && r.index == r.script.errAt {
		return r.script.err
	}
	if r.index >= len(r.script.values) {
		return io.EOF
	}
	copy(dest, r.script.values[r.index])
	r.index++
	return nil
}

var cityDriverSeq atomic.Uint64

func openScriptedCityDB(t *testing.T, scripts ...cityRowsScript) (*sql.DB, *scriptedCityDriver) {
	t.Helper()
	d := &scriptedCityDriver{scripts: scripts}
	name := fmt.Sprintf("v2-city-script-%d", cityDriverSeq.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, d
}

func cityRequest(handler http.Handler, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v2/board/cities", nil)
	req.RemoteAddr = remoteAddr
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestCitySummaryHandlerScanErrorNoStoreAndNoCache(t *testing.T) {
	db, scripted := openScriptedCityDB(t,
		cityRowsScript{values: [][]driver.Value{{"tbilisi", "not-an-integer"}}, errAt: -1},
		cityRowsScript{values: [][]driver.Value{{"tbilisi", int64(2)}}, errAt: -1},
	)
	h := NewCitySummaryHandler(db, func() time.Time { return time.Unix(1_700_000_000, 0) }).Routes()

	failed := cityRequest(h, "203.0.113.10:41000")
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("scan failure status: got %d, want 500; body=%s", failed.Code, failed.Body)
	}
	if got := failed.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("scan failure Cache-Control: got %q, want no-store", got)
	}

	retried := cityRequest(h, "203.0.113.10:41001")
	if retried.Code != http.StatusOK {
		t.Fatalf("retry status: got %d, want 200; body=%s", retried.Code, retried.Body)
	}
	if scripted.queries != 2 {
		t.Fatalf("failed partial result was cached: query count=%d, want 2", scripted.queries)
	}
	if body := retried.Body.String(); !containsAll(body, `"country_label":"Georgia"`, `"active_count":2`) {
		t.Fatalf("successful response missing backend country label/count: %s", body)
	}
}

func TestCitySummaryHandlerRowsErrorNoStoreAndNoCache(t *testing.T) {
	db, scripted := openScriptedCityDB(t,
		cityRowsScript{
			values: [][]driver.Value{{"tbilisi", int64(1)}},
			errAt:  1,
			err:    errors.New("late rows failure"),
		},
		cityRowsScript{values: [][]driver.Value{{"tbilisi", int64(1)}}, errAt: -1},
	)
	h := NewCitySummaryHandler(db, func() time.Time { return time.Unix(1_700_000_000, 0) }).Routes()

	failed := cityRequest(h, "203.0.113.11:42000")
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("rows failure status: got %d, want 500; body=%s", failed.Code, failed.Body)
	}
	if got := failed.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("rows failure Cache-Control: got %q, want no-store", got)
	}

	retried := cityRequest(h, "203.0.113.11:42001")
	if retried.Code != http.StatusOK {
		t.Fatalf("retry status: got %d, want 200; body=%s", retried.Code, retried.Body)
	}
	if scripted.queries != 2 {
		t.Fatalf("failed rows result was cached: query count=%d, want 2", scripted.queries)
	}
}

func TestCitySummaryHandlerRateLimitUsesHostWithoutPort(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := NewCitySummaryHandler(db, func() time.Time { return time.Unix(1_700_000_000, 0) }).Routes()

	for i := 0; i < 60; i++ {
		rr := cityRequest(h, fmt.Sprintf("203.0.113.12:%d", 43000+i))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status: got %d, want 200", i+1, rr.Code)
		}
	}
	limited := cityRequest(h, "203.0.113.12:44000")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("same host with changing ports bypassed limiter: got %d, want 429", limited.Code)
	}
	if got := limited.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("rate-limit Cache-Control: got %q, want no-store", got)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
