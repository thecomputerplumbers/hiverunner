package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/api"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
)

// signIn has a person say who they are in a browser, and returns a token
// good for the one request that follows: the server ends the session as it
// answers. Signing in says nothing about which hives a machine serves.
func signIn(ctx context.Context, client *api.Client, noBrowser bool) (string, error) {
	code, err := client.StartDevice(ctx)
	if err != nil {
		return "", err
	}
	fmt.Printf("\nTo add this machine, approve this code in your browser:\n\n    %s\n\n%s\n\n", code.UserCode, code.VerificationURIComplete)
	if !noBrowser {
		openBrowser(code.VerificationURIComplete)
	}
	fmt.Println("Waiting for approval...")
	return client.AwaitToken(ctx, code)
}

// link adds this machine, already enrolled, to more of its owner's hives.
// Its owner approves in a browser, as they did to enroll it: the token on
// this disk alone must not be able to bring another organization's jobs here.
func link(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("link", flag.ExitOnError)
	noBrowser := flags.Bool("no-browser", false, "print the sign-in link instead of opening it")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, "hived link [--no-browser] [hive ...]\n\n"+
			"Adds this machine to the hives named, or asks which if none are.\n"+
			"A hive is named as GitHub names the organization.\n")
	}
	_ = flags.Parse(args)
	wanted := flags.Args()

	machine, err := config.Load()
	if err != nil {
		return err
	}
	serving, err := served(ctx, machine)
	if err != nil {
		return err
	}
	fmt.Println(serving)

	client := api.New(machine.Server)
	token, err := signIn(ctx, client, *noBrowser)
	if err != nil {
		return err
	}
	linked, err := client.Link(ctx, token, machine.MachineID, wanted)
	var choose *api.ChoosePoolError
	if errors.As(err, &choose) {
		var open []api.Pool
		for _, pool := range choose.Pools {
			if !pool.Linked {
				open = append(open, pool)
			}
		}
		chosen, err := askPools("Which other hives should this machine serve?", open, func(pool api.Pool) string {
			return "hived link " + pool.Name
		})
		if err != nil {
			return err
		}
		// A second sign-in is not needed: a choice being offered is the one
		// answer that leaves the session open.
		linked, err = client.Link(ctx, token, machine.MachineID, ids(chosen))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	for _, refused := range linked.Refused {
		fmt.Printf("Not added to %s: %s\n", refused.Name, refused.Message)
	}
	switch {
	case len(linked.Added) > 0:
		fmt.Printf("\nAdded to: %s\n", names(linked.Added))
		fmt.Println("A daemon that is running picks that up within a minute. Nothing to restart.")
	case len(linked.Refused) > 0:
		return errors.New("this machine was not added to a hive")
	case len(wanted) > 0:
		fmt.Println("\nThis machine serves those already.")
	default:
		fmt.Println("\nThis machine already serves every hive you belong to.")
		fmt.Printf("A hive is a GitHub organization with HiveWorker installed: %s/install\n", client.Server)
	}
	fmt.Println(describe(linked.Pools))
	return nil
}

// unlink stops this machine serving one hive. It keeps serving the others,
// and stays signed in.
func unlink(ctx context.Context, args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("say which hive: hived unlink <hive>\nSee the ones this machine serves with: hived hives")
	}
	machine, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := api.New(machine.Server)
	assignment, err := client.Me(ctx, machine.Token)
	if err != nil {
		return err
	}
	for _, pool := range assignment.Pools {
		if pool.ID != args[0] && !strings.EqualFold(pool.Name, args[0]) {
			continue
		}
		left, err := client.Unlink(ctx, machine.Token, pool.ID)
		if err != nil {
			return err
		}
		fmt.Printf("No longer serving %s.\n", pool.Name)
		fmt.Println(describe(left))
		return nil
	}
	return fmt.Errorf("this machine does not serve a hive called %q. %s", args[0], describe(assignment.Pools))
}

// hives prints the hives this machine serves, one to a line, for scripts.
func hives(ctx context.Context) error {
	machine, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	assignment, err := api.New(machine.Server).Me(ctx, machine.Token)
	if err != nil {
		return err
	}
	for _, pool := range assignment.Pools {
		fmt.Println(pool.Name)
	}
	return nil
}

// served is a sentence saying which hives the machine serves right now.
func served(ctx context.Context, machine config.Machine) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	assignment, err := api.New(machine.Server).Me(ctx, machine.Token)
	if err != nil {
		return "", err
	}
	return describe(assignment.Pools), nil
}

func describe(pools []api.Pool) string {
	if len(pools) == 0 {
		return "It serves no hives."
	}
	return "It serves: " + names(pools)
}

func names(pools []api.Pool) string {
	out := make([]string, len(pools))
	for i, pool := range pools {
		out[i] = pool.Name
	}
	return strings.Join(out, ", ")
}

func ids(pools []api.Pool) []string {
	out := make([]string, len(pools))
	for i, pool := range pools {
		out[i] = pool.ID
	}
	return out
}

// askPools lets someone in several organizations choose which of them this
// machine should serve. It can serve all of them at once.
func askPools(question string, pools []api.Pool, again func(api.Pool) string) ([]api.Pool, error) {
	fmt.Println("\n" + question)
	for i, pool := range pools {
		fmt.Printf("  %d. %s\n", i+1, pool.Name)
	}
	fmt.Print("Numbers separated by commas, or \"all\": ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fmt.Println()
		return nil, unanswered(pools, again)
	}
	return choosePools(strings.TrimSpace(line), pools)
}

// unanswered is what to say when nobody was there to choose, as when the
// command was started by a script. The approval is spent by then, so it
// gives the command that names each hive and needs no question.
func unanswered(pools []api.Pool, again func(api.Pool) string) error {
	lines := []string{"no hive was chosen, because there was no one to ask. Run it again and name the hive:"}
	for _, pool := range pools {
		lines = append(lines, "  "+again(pool))
	}
	return errors.New(strings.Join(lines, "\n"))
}

func choosePools(answer string, pools []api.Pool) ([]api.Pool, error) {
	if strings.EqualFold(answer, "all") {
		return pools, nil
	}
	var chosen []api.Pool
	for _, part := range strings.Split(answer, ",") {
		choice, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || choice < 1 || choice > len(pools) {
			return nil, errors.New("that is not one of the hives listed")
		}
		chosen = append(chosen, pools[choice-1])
	}
	return chosen, nil
}
