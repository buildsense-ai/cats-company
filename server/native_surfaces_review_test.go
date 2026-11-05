package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type surfaceReadBarrier struct {
	*surfacePushStore
	paused  atomic.Bool
	started chan struct{}
	resume  chan struct{}
}

func (s *surfaceReadBarrier) ListPushSubscriptions(ctx context.Context, uid int64) ([]*types.PushSubscription, error) {
	rows, err := s.surfacePushStore.ListPushSubscriptions(ctx, uid)
	if s.paused.CompareAndSwap(false, true) {
		close(s.started)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return rows, err
}

func TestNativeSurfaceEnrollmentCreationRaces(t *testing.T) {
	for _, mode := range []string{"absent-activity", "absent-widget", "expired-widget", "absent-widget-same", "expired-widget-same"} {
		t.Run(mode, func(t *testing.T) {
			h, rows, db := surfaceFixture(t)
			activity := activityInput()
			widget := surfaceRegistrationRequest{AccessToken: strings.Repeat("ab", 32), RegistrationID: activity.RegistrationID, Scope: strings.Repeat("cd", 32)}
			isActivity := mode == "absent-activity"
			enroll := func() *httptest.ResponseRecorder {
				if isActivity {
					return surfaceRequest(t, h.HandleActivities, "POST", 7, activity, "")
				}
				return surfaceRequest(t, h.HandleSurfaces, "POST", 7, widget, "")
			}
			if strings.HasPrefix(mode, "expired-widget") {
				if w := enroll(); w.Code != 201 {
					t.Fatal(w.Code)
				}
				for _, row := range rows.rows {
					data, _ := json.Marshal(surfaceRegistrationMetadata{Scope: widget.Scope, Expires: time.Now().Add(-time.Hour).Unix()})
					row.P256DH = string(data)
				}
			}
			barrier := &surfaceReadBarrier{surfacePushStore: rows, started: make(chan struct{}), resume: make(chan struct{})}
			h.push.store = barrier
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- enroll() }()
			<-barrier.started // A has observed the absent/expired version.
			if w := enroll(); w.Code != 201 {
				t.Fatal("B enrollment", w.Code)
			}
			if isActivity {
				h.push.client.Transport = apnsRoundTripper(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
				})
				if err := h.push.sendActivityStatus(context.Background(), db.taskStatuses[activity.TopicID]); err != nil {
					t.Fatal(err)
				}
			} else if !strings.HasSuffix(mode, "same") {
				rotated := widget
				rotated.AccessToken = strings.Repeat("ef", 32)
				if w := surfaceRequest(t, h.HandleSurfaces, "POST", 7, rotated, ""); w.Code != 201 {
					t.Fatal("rotation", w.Code)
				}
			}
			before, _ := rows.ListPushSubscriptions(context.Background(), 7)
			close(barrier.resume)
			select {
			case w := <-done:
				want := 409
				if isActivity || strings.HasSuffix(mode, "same") {
					want = 201
				}
				if w.Code != want {
					t.Fatalf("A status=%d want=%d", w.Code, want)
				}
			case <-time.After(time.Second):
				t.Fatal("enrollment blocked")
			}
			after, _ := rows.ListPushSubscriptions(context.Background(), 7)
			if len(after) != 1 || after[0].Auth != before[0].Auth || after[0].P256DH != before[0].P256DH || after[0].RegistrationID != before[0].RegistrationID {
				t.Fatal("stale enrollment reset a watermark, expiry or secret", after, before)
			}
		})
	}
}

func TestNativeSurfaceOverlappingRunCompletionEvent(t *testing.T) {
	h, _, db := surfaceFixture(t)
	input := activityInput()
	a := *db.taskStatuses[input.TopicID]
	a.RunID = "older-A"
	b := a
	b.RunID = input.RunID
	b.EventUpdatedAt = a.EventUpdatedAt.Add(time.Millisecond)
	b.UpdatedAt = b.EventUpdatedAt
	db.runs = map[string]*types.ConversationTaskStatus{a.RunID: &a, b.RunID: &b}
	db.taskStatuses[input.TopicID] = &b // A running, then newer B running.
	if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 201 {
		t.Fatal(w.Code)
	}
	delivered := make(chan string, 2)
	h.push.client.Transport = apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			APS struct {
				Event   string               `json:"event"`
				Content activityContentState `json:"content-state"`
			} `json:"aps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.APS.Content.State != "completed" || r.Header.Get("apns-push-type") != "liveactivity" {
			t.Error("wrong completion payload")
		}
		delivered <- payload.APS.Event
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	b.State = "completed"
	b.EventUpdatedAt = b.EventUpdatedAt.Add(time.Millisecond)
	b.UpdatedAt = b.EventUpdatedAt
	db.taskStatuses[input.TopicID] = &a  // Source projection falls back to older active A.
	h.hub.observeAgentPushTaskStatus(&b) // The accepted completion takes the real queued hook.
	select {
	case event := <-delivered:
		if event != "end" {
			t.Fatal("completion did not end B", event)
		}
	case <-time.After(time.Second):
		t.Fatal("B completion lost behind older A")
	}
	close(h.push.activityQueue)
	close(h.push.queue)
}

func TestNativeSurfaceLateUnseenTerminalKeepsRegisteredRun(t *testing.T) {
	h, rows, db := surfaceFixture(t)
	input := activityInput()
	b := *db.taskStatuses[input.TopicID]
	db.runs = map[string]*types.ConversationTaskStatus{b.RunID: &b}
	db.taskStatuses[input.TopicID] = &b
	if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 201 {
		t.Fatal(w.Code)
	}
	var events []string
	h.push.client.Transport = apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			APS struct {
				Event   string               `json:"event"`
				Content activityContentState `json:"content-state"`
			} `json:"aps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		events = append(events, payload.APS.Event+":"+payload.APS.Content.State)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	// First-seen A is ingested after enrollment, but its publisher event T1
	// precedes B's T2. The active-first source projection correctly retains B.
	a := b
	a.RunID, a.State = "late-first-seen-A", "completed"
	a.EventUpdatedAt = b.EventUpdatedAt.Add(-time.Minute)
	a.UpdatedAt = b.UpdatedAt.Add(time.Millisecond)
	db.runs[a.RunID] = &a
	if err := h.push.sendActivityStatus(context.Background(), &a); err != nil {
		t.Fatal(err)
	}
	active, _ := rows.ListPushSubscriptions(context.Background(), 7)
	if len(active) != 1 || len(events) != 0 {
		t.Fatal("older terminal receipt retired or updated B", active, events)
	}
	// Fresh enrollment must remain valid despite A's later insertion.
	if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 201 {
		t.Fatal("late A prevented B enrollment", w.Code)
	}
	for _, state := range []string{"waiting", "completed"} {
		b.State = state
		b.EventUpdatedAt = b.EventUpdatedAt.Add(time.Second)
		b.UpdatedAt = b.UpdatedAt.Add(time.Second)
		if err := h.push.sendActivityStatus(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(events, ",") != "update:waiting,end:completed" {
		t.Fatal("B did not continue through its own terminal event", events)
	}
	active, _ = rows.ListPushSubscriptions(context.Background(), 7)
	if len(active) != 0 {
		t.Fatal("ended B registration retained")
	}
}

func TestNativeSurfaceObservedRenewalCannotRecreateAfterDelete(t *testing.T) {
	for _, kind := range []string{"expired-widget", "activity-details"} {
		t.Run(kind, func(t *testing.T) {
			h, rows, db := surfaceFixture(t)
			activity := activityInput()
			widget := surfaceRegistrationRequest{AccessToken: strings.Repeat("ab", 32), RegistrationID: activity.RegistrationID, Scope: strings.Repeat("cd", 32)}
			request := func(method string) *httptest.ResponseRecorder {
				if kind == "activity-details" {
					return surfaceRequest(t, h.HandleActivities, method, 7, activity, "")
				}
				return surfaceRequest(t, h.HandleSurfaces, method, 7, widget, "")
			}
			if w := request("POST"); w.Code != 201 {
				t.Fatal(w.Code)
			}
			if kind == "expired-widget" {
				for _, row := range rows.rows {
					data, _ := json.Marshal(surfaceRegistrationMetadata{Scope: widget.Scope, Expires: time.Now().Add(-time.Hour).Unix()})
					row.P256DH = string(data)
				}
			} else {
				activity.Details = true // Require a metadata CAS, not a no-op refresh.
			}
			barrier := &surfaceReadBarrier{surfacePushStore: rows, started: make(chan struct{}), resume: make(chan struct{})}
			h.push.store = barrier
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- request("POST") }()
			<-barrier.started // POST has captured the existing version.
			if w := request("DELETE"); w.Code != 200 {
				t.Fatal("revocation", w.Code)
			}
			close(barrier.resume)
			select {
			case w := <-done:
				if w.Code != 409 {
					t.Fatal("stale renewal did not conflict after revocation", w.Code)
				}
			case <-time.After(time.Second):
				t.Fatal("stale renewal blocked")
			}
			active, _ := rows.ListPushSubscriptions(context.Background(), 7)
			if len(active) != 0 {
				t.Fatal("stale renewal recreated revoked row", active)
			}
			if kind == "expired-widget" {
				if w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface 7."+widget.AccessToken); w.Code != 401 {
					t.Fatal("revoked capability authorized snapshot", w.Code)
				}
			} else {
				calls := 0
				h.push.client.Transport = apnsRoundTripper(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
				})
				if err := h.push.sendActivityStatus(context.Background(), db.taskStatuses[activity.TopicID]); err != nil || calls != 0 {
					t.Fatal("revoked activity delivered", calls, err)
				}
			}
		})
	}
}

func TestNativeSurfaceTransientGroupReadsPreserveRegistration(t *testing.T) {
	for _, failure := range []string{"managed-group", "membership"} {
		t.Run(failure, func(t *testing.T) {
			h, rows, db := surfaceFixture(t)
			input := activityInput()
			input.TopicID = "grp_80"
			status := *db.taskStatuses["p2p_7_8"]
			status.TopicID = input.TopicID
			db.taskStatuses[input.TopicID] = &status
			db.groupMembership = map[int64]bool{80: true}
			if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 201 {
				t.Fatal(w.Code)
			}
			db.groupFailure = failure
			calls := 0
			h.push.client.Transport = apnsRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			if err := h.push.sendActivityStatus(context.Background(), &status); err == nil {
				t.Fatal("lookup error swallowed")
			}
			active, _ := rows.ListPushSubscriptions(context.Background(), 7)
			if len(active) != 1 || calls != 0 {
				t.Fatal("transient failure revoked or delivered")
			}
			db.groupFailure = ""
			if err := h.push.sendActivityStatus(context.Background(), &status); err != nil || calls != 1 {
				t.Fatal("recovery", calls, err)
			}
		})
	}
}

func TestNativeSurfaceCancellationAndAlertWorkerIsolation(t *testing.T) {
	h, rows, db := surfaceFixture(t)
	started, canceled := make(chan struct{}, 3), make(chan struct{}, 3)
	db.blockRead = func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		return ctx.Err()
	}
	event := *db.taskStatuses["p2p_7_8"]
	event.TopicID = "grp_80"
	// No activity registrations. Discovery itself must obey cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.push.sendActivityStatus(ctx, &event) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read ignored cancellation")
	}
	<-canceled
	h.push.startNativeWorkers()
	// Saturate both activity workers with reads expiring on their job deadlines.
	for range 2 {
		h.push.activityQueue <- nativeDeliveryJob{pushDeliveryJob: pushDeliveryJob{queuedAt: time.Now().Add(-pushDeliveryTimeout + time.Second)}, status: &event}
	}
	for range 2 {
		<-started
	}
	returned := make(chan struct{})
	go func() { h.hub.notifyNativeTaskStatus(&event); close(returned) }()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("task callback blocked on activity discovery")
	}
	// Both workers remain inside discovery; inspect the callback's queued job.
	select {
	case <-h.push.activityQueue:
	default:
		t.Fatal("callback did not enqueue activity")
	}

	ordinary := &types.PushSubscription{UID: 7, Endpoint: "apns://production/cc.catsco.ios/" + strings.Repeat("ab", 32), Auth: "apns-v1", P256DH: "generic", RegistrationID: "registration-current"}
	rows.UpsertPushSubscription(context.Background(), ordinary, 10)
	alert := make(chan struct{}, 1)
	h.push.client.Transport = apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("apns-push-type") == "alert" {
			alert <- struct{}{}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if !h.push.Enqueue(7, PushNotification{Body: "ordinary alert"}, nil) {
		t.Fatal("alert enqueue failed")
	}
	select {
	case <-alert:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("activity reads monopolized alert workers")
	}
	for range 2 {
		select {
		case <-canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("activity deadline did not cancel discovery")
		}
	}
	// Already canceled contexts must stop before starting any discovery.
	if err := h.push.sendActivityStatus(ctx, &event); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(h.push.activityQueue)
	close(h.push.queue)
}
