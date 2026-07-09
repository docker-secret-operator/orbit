package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rolloutapi "github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"go.uber.org/zap"
)

// newRecoveryTestAPI mirrors newTestAPI in control_test.go but also returns
// the *ControlServer itself, since these tests need to call
// SetRecoveryTrigger on the exact instance the test server wraps.
func newRecoveryTestAPI(t *testing.T, token string) (*rolloutapi.ControlServer, *httptest.Server) {
	t.Helper()
	m := metrics.New()
	reg := proxy.NewRegistry()
	srv := proxy.NewServer(zap.NewNop(), m)
	t.Cleanup(srv.Close)

	cs := rolloutapi.NewControlServer(reg, srv, zap.NewNop(), m, token, nil)
	cs.SetStartupState(proxy.StartupReady)
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	return cs, ts
}

func TestRecover_NotWired_Returns503(t *testing.T) {
	_, ts := newRecoveryTestAPI(t, "")

	resp, err := http.Post(ts.URL+"/recover", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when no RecoveryFunc is wired", resp.StatusCode)
	}
}

func TestRecover_WrongMethod_405(t *testing.T) {
	_, ts := newRecoveryTestAPI(t, "")

	resp, err := http.Get(ts.URL + "/recover")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for GET /recover", resp.StatusCode)
	}
}

func TestRecover_Success_ReturnsOutcome(t *testing.T) {
	cs, ts := newRecoveryTestAPI(t, "")

	want := rolloutapi.RecoveryOutcome{
		Timestamp:               time.Now().UTC(),
		Epoch:                   7,
		Action:                  "restore_single",
		AuthoritativeGeneration: "gen-2",
		Reason:                  "restore authoritative generation: gen-2",
		BackendsRestored:        1,
		ProxyStatus:             "ready",
	}
	cs.SetRecoveryTrigger(func(ctx context.Context) (rolloutapi.RecoveryOutcome, error) {
		return want, nil
	})

	resp, err := http.Post(ts.URL+"/recover", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got rolloutapi.RecoveryOutcome
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Epoch != want.Epoch || got.Action != want.Action || got.AuthoritativeGeneration != want.AuthoritativeGeneration {
		t.Errorf("decoded outcome = %+v, want %+v", got, want)
	}
}

func TestRecover_TriggerError_Returns500(t *testing.T) {
	cs, ts := newRecoveryTestAPI(t, "")
	cs.SetRecoveryTrigger(func(ctx context.Context) (rolloutapi.RecoveryOutcome, error) {
		return rolloutapi.RecoveryOutcome{}, context.DeadlineExceeded
	})

	resp, err := http.Post(ts.URL+"/recover", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when the trigger itself errors", resp.StatusCode)
	}
}

func TestRecover_ConcurrentCallsAreSerialized(t *testing.T) {
	cs, ts := newRecoveryTestAPI(t, "")

	started := make(chan struct{})
	release := make(chan struct{})
	cs.SetRecoveryTrigger(func(ctx context.Context) (rolloutapi.RecoveryOutcome, error) {
		started <- struct{}{}
		<-release
		return rolloutapi.RecoveryOutcome{ProxyStatus: "ready"}, nil
	})

	// First request: let it block inside the trigger.
	respCh := make(chan *http.Response, 1)
	go func() {
		resp, _ := http.Post(ts.URL+"/recover", "application/json", nil) //nolint:errcheck
		respCh <- resp
	}()
	<-started

	// Second request while the first is still in flight: must be rejected.
	resp2, err := http.Post(ts.URL+"/recover", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("concurrent call status = %d, want 409", resp2.StatusCode)
	}

	close(release)
	resp1 := <-respCh
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("first call status = %d, want 200", resp1.StatusCode)
	}
}

func TestRecover_RequiresAuthWhenTokenSet(t *testing.T) {
	cs, ts := newRecoveryTestAPI(t, "secret-token")
	cs.SetRecoveryTrigger(func(ctx context.Context) (rolloutapi.RecoveryOutcome, error) {
		return rolloutapi.RecoveryOutcome{ProxyStatus: "ready"}, nil
	})

	resp, err := http.Post(ts.URL+"/recover", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status without token = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/recover", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status with correct token = %d, want 200", resp2.StatusCode)
	}
}
