package protocol

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func fixtures(t *testing.T, name string) []json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../../packages/protocol/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		t.Fatalf("%s has no messages", name)
	}
	return messages
}

func TestEveryCoordinatorFixtureDecodes(t *testing.T) {
	seen := map[string]bool{}
	for _, raw := range fixtures(t, "coordinator.json") {
		message, err := DecodeCoordinator(raw)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		seen[reflect.TypeOf(message).Elem().Name()] = true
	}
	for _, want := range []string{"Welcome", "JobAssign", "JobStart", "JobRelease", "Drain", "Error"} {
		if !seen[want] {
			t.Errorf("no fixture exercised %s", want)
		}
	}
}

// What the daemon sends must be byte-for-byte what the coordinator's own
// fixtures say a daemon sends.
func TestDaemonMessagesMatchFixtures(t *testing.T) {
	limited := NewHello("0.1.0", Online)
	limited.MaxVCPUs = 4
	limited.MaxJobs = 2
	box := NewHello("0.2.0", Online)
	box.Arch = ArchX64
	sent := []any{
		NewHello("0.1.0", Online),
		NewHello("0.1.0", Busy, "a-1", "a-2"),
		limited,
		box,
		NewPresence(Paused),
		NewPresence(Draining),
		NewJobAccepted("a-1"),
		NewJobRejected("a-1", "lid closed"),
		NewJobExited("a-1", 0),
	}
	want := fixtures(t, "daemon.json")
	if len(sent) != len(want) {
		t.Fatalf("fixtures have %d messages, test builds %d", len(want), len(sent))
	}
	for i, message := range sent {
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var got, expected any
		_ = json.Unmarshal(encoded, &got)
		_ = json.Unmarshal(want[i], &expected)
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("message %d: sent %s, fixture %s", i, encoded, want[i])
		}
	}
}

// An offer says which architecture its label names; one from a coordinator
// that predates a second architecture says nothing.
func TestAnOfferCarriesItsArchitecture(t *testing.T) {
	var arches []string
	for _, raw := range fixtures(t, "coordinator.json") {
		message, err := DecodeCoordinator(raw)
		if err != nil {
			t.Fatal(err)
		}
		if assign, ok := message.(*JobAssign); ok {
			arches = append(arches, assign.Label+"="+assign.Arch)
		}
	}
	if want := []string{"hive=", "hive-x64-4cpu=x64"}; !reflect.DeepEqual(arches, want) {
		t.Fatalf("offers in the fixtures = %v, want %v", arches, want)
	}
}

func TestUnknownAndIncompleteFramesAreErrors(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`{"type":"reboot"}`,
		`{"type":"job.assign","assignmentId":"a"}`,
		`{"type":"job.assign","assignmentId":"","vcpus":2}`,
		`{"type":"job.start","assignmentId":"a","runnerName":"r"}`,
		`{"type":"job.start","assignmentId":"a","jitConfig":"x"}`,
		`{"type":"job.release"}`,
	} {
		if _, err := DecodeCoordinator([]byte(raw)); err == nil {
			t.Errorf("%s decoded without error", raw)
		}
	}
}
