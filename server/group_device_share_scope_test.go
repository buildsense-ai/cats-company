package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type previewScopeTestStore struct {
	*groupSharingTestStore
	writes int
}

func (s *previewScopeTestStore) GetGroupDeviceShareRequest(context.Context, string) (*store.GroupDeviceShareRequest, error) {
	return &store.GroupDeviceShareRequest{ID: "preview-request", OwnerUID: 8, RequesterUID: 7, TopicID: "grp_1", Status: "pending", Revision: 1}, nil
}
func (s *previewScopeTestStore) SaveGroupDeviceShare(context.Context, store.GroupDeviceShare, int64) (*store.GroupDeviceShare, error) {
	s.writes++
	return nil, errors.New("unexpected permission write")
}
func (s *previewScopeTestStore) ResolveGroupDeviceShareRequest(context.Context, string, int64, string, *store.GroupDeviceShare) (*store.GroupDeviceShareRequest, error) {
	s.writes++
	return nil, errors.New("unexpected approval write")
}

func TestGroupShareConfirmationChecksPreviewedOperations(t *testing.T) {
	for _, action := range []string{"share", "approve"} {
		t.Run(action, func(t *testing.T) {
			h, _, base := groupShareCompatibilityFixture(t)
			db := &previewScopeTestStore{groupSharingTestStore: base}
			h.db = db
			body := `{"action":"` + action + `","topic_id":"grp_1","grantee_uid":7,"device_id":"shared-pc","request_id":"preview-request","expected_revision":1,"expected_operations":["read_file","execute_shell"]}`
			request := httptest.NewRequest("POST", "/", strings.NewReader(body))
			request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(8)))
			result := httptest.NewRecorder()
			handler := NewGroupDeviceShareHandler(h)
			if action == "share" {
				handler.HandleShares(result, request)
			} else {
				handler.HandleRequests(result, request)
			}
			if result.Code != 409 || db.writes != 0 {
				t.Fatalf("changed capability scope wrote a permission: status=%d writes=%d body=%s", result.Code, db.writes, result.Body.String())
			}
		})
	}
}
func TestGroupShareOperationSnapshotUsesGrantFilter(t *testing.T) {
	snapshot := groupShareOperationSnapshot([]UserDevice{{DeviceID: "pc", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile, DeviceGrantExecuteShell, DeviceGrantReadFile, "unknown_operation", DeviceGrantBrowserControl}}})
	if !reflect.DeepEqual(snapshot["pc"], []string{"read_file", "execute_shell"}) {
		t.Fatal("scope preview differs from grant operations", snapshot)
	}
	for _, tc := range []struct {
		expected []string
		want     bool
	}{{nil, true}, {[]string{"execute_shell", "read_file"}, true}, {[]string{}, false}, {[]string{"read_file", "read_file"}, false}, {[]string{"read_file"}, false}} {
		if got := sameShareOperations(tc.expected, snapshot["pc"]); got != tc.want {
			t.Fatalf("scope %v: got %t want %t", tc.expected, got, tc.want)
		}
	}
}
