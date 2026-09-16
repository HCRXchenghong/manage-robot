package main

import (
	"testing"

	platformv1 "robot-agent/protocols/platform/v1"
)

func TestNavigationAckTransitionRejectsBackwardsAndUnconfirmedCancel(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		action platformv1.NavigationAction
		result platformv1.NavigationResult
		want   string
		ok     bool
	}{
		{"broker delivery can be accepted", "dispatched", platformv1.NavigationAction_NAVIGATION_ACTION_DISPATCH, platformv1.NavigationResult_NAVIGATION_RESULT_ACCEPTED, "accepted", true},
		{"accepted can start", "accepted", platformv1.NavigationAction_NAVIGATION_ACTION_DISPATCH, platformv1.NavigationResult_NAVIGATION_RESULT_STARTED, "running", true},
		{"running can complete", "running", platformv1.NavigationAction_NAVIGATION_ACTION_DISPATCH, platformv1.NavigationResult_NAVIGATION_RESULT_COMPLETED, "completed", true},
		{"completed cannot go back to running", "completed", platformv1.NavigationAction_NAVIGATION_ACTION_DISPATCH, platformv1.NavigationResult_NAVIGATION_RESULT_STARTED, "", false},
		{"cancel request is not cancellation", "cancel_requested", platformv1.NavigationAction_NAVIGATION_ACTION_CANCEL, platformv1.NavigationResult_NAVIGATION_RESULT_ACCEPTED, "cancel_requested", true},
		{"vehicle confirms cancellation", "cancel_requested", platformv1.NavigationAction_NAVIGATION_ACTION_CANCEL, platformv1.NavigationResult_NAVIGATION_RESULT_CANCELLED, "cancelled", true},
		{"cancel rejection is visible", "cancel_requested", platformv1.NavigationAction_NAVIGATION_ACTION_CANCEL, platformv1.NavigationResult_NAVIGATION_RESULT_REJECTED, "cancel_rejected", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := navigationAckTransition(tc.state, tc.action, tc.result)
			if tc.ok {
				if err != nil || got != tc.want {
					t.Fatalf("transition=%q err=%v want=%q", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid transition was accepted as %q", got)
			}
		})
	}
}

func TestNavigationCancelRemainsPendingUntilVehicleAck(t *testing.T) {
	st := NewState(NewHub(), nil)
	ns := NewNavStore(st)
	ns.publish = func(*platformv1.NavigationCommand) bool { return true }
	st.mu.Lock()
	st.vehicles["vehicle-cancel-test"] = &vehicleState{id: "vehicle-cancel-test", gatewayID: "gateway-cancel-test"}
	st.mu.Unlock()
	route, err := ns.Submit("vehicle-cancel-test", "巡检", "test", "trace", "", []NavPoint{{Name: "A"}, {Name: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ns.Cancel(route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "cancel_requested" {
		t.Fatalf("cancel was reported as completed before vehicle ACK: %q", got.Status)
	}
	if current, _ := ns.Get(route.ID); current.Status != "cancel_requested" {
		t.Fatalf("in-memory cancel state=%q", current.Status)
	}
}
