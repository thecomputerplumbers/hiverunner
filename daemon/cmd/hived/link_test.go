package main

import (
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/api"
)

func TestChoosingHivesByNumber(t *testing.T) {
	pools := []api.Pool{{ID: "gh-1", Name: "acme"}, {ID: "gh-2", Name: "globex"}, {ID: "gh-3", Name: "initech"}}
	for answer, want := range map[string]string{
		"2":     "globex",
		"3, 1":  "initech, acme",
		"all":   "acme, globex, initech",
		"ALL":   "acme, globex, initech",
		" 1 ,2": "acme, globex",
	} {
		chosen, err := choosePools(answer, pools)
		if err != nil || names(chosen) != want {
			t.Errorf("%q chose %q, %v; want %q", answer, names(chosen), err, want)
		}
	}
	for _, answer := range []string{"", "0", "4", "globex", "1,,2"} {
		if chosen, err := choosePools(answer, pools); err == nil {
			t.Errorf("%q chose %q; want a refusal", answer, names(chosen))
		}
	}
	if got := describe(nil); got != "It serves no hives." {
		t.Errorf("describe(nil) = %q", got)
	}
}

func TestEnrollingAfterChoosingHivesReturnsWhatTheSecondAttemptSaid(t *testing.T) {
	pools := []api.Pool{{ID: "gh-1", Name: "acme"}, {ID: "gh-2", Name: "globex"}}
	var asked [][]string
	enroll := func(machine api.Machine) (api.Enrollment, error) {
		asked = append(asked, machine.OrganizationIDs)
		if len(machine.OrganizationIDs) == 0 {
			return api.Enrollment{}, &api.ChoosePoolError{Pools: pools}
		}
		return api.Enrollment{MachineID: "m-1", Token: "secret", Pools: pools}, nil
	}
	all := func(offered []api.Pool) ([]api.Pool, error) { return offered, nil }

	enrollment, err := enrollChoosing(api.Machine{Name: "box"}, enroll, all)
	if err != nil || enrollment.Token != "secret" {
		t.Fatalf("enrolled as %+v, %v; want the second attempt's token", enrollment, err)
	}
	if len(asked) != 2 || len(asked[1]) != 2 || asked[1][0] != "gh-1" || asked[1][1] != "gh-2" {
		t.Fatalf("the server was asked %v; want once with no hives, then with both", asked)
	}

	// A hive named up front is never asked about.
	asked = nil
	never := func([]api.Pool) ([]api.Pool, error) { t.Fatal("asked which hive"); return nil, nil }
	if _, err := enrollChoosing(api.Machine{OrganizationIDs: []string{"gh-2"}}, enroll, never); err != nil || len(asked) != 1 {
		t.Fatalf("asked %v, %v; want one attempt", asked, err)
	}
}

func TestAQuestionNobodyAnswersSaysTheCommandForEachHive(t *testing.T) {
	pools := []api.Pool{{ID: "gh-1", Name: "acme"}, {ID: "gh-2", Name: "globex"}}
	err := unanswered(pools, func(pool api.Pool) string { return "hived login --hive " + pool.ID })
	for _, want := range []string{"hived login --hive gh-1", "hived login --hive gh-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}
