package leg

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as a fake vcs-loadbot: the calibration test re-executes
// this test binary with LEG_FAKE_LOADBOT=1 as its LoadbotBin.
func TestMain(m *testing.M) {
	if os.Getenv("LEG_FAKE_LOADBOT") == "1" {
		os.Exit(fakeLoadbot(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func flagVal(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func fakeLoadbot(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "reflect":
		if flagVal(args, "--listen") != "127.0.0.1:0" || flagVal(args, "--senders") != "2" {
			fmt.Fprintln(os.Stderr, "fake reflect: bad flags", args)
			return 2
		}
		fmt.Println(`{"type":"ready","addr":"127.0.0.1:45678"}`)
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"quit"`) {
				break
			}
		}
		fmt.Println(`{"type":"reflector_stats","send_errors":4,"senders":2}`)
		return 0
	case "calibrate":
		if flagVal(args, "--reflector") != "127.0.0.1:45678" || flagVal(args, "--allow") != "127.0.0.1:45678" ||
			flagVal(args, "--steps") != "64,128" || flagVal(args, "--hold") != "3" || flagVal(args, "--seed") != "7" {
			fmt.Fprintln(os.Stderr, "fake calibrate: bad flags", args)
			return 2
		}
		fmt.Println(`{"type":"calibration","n":64,"egress_pps":3150,"valid":true,"violations":null}`)
		fmt.Println(`{"type":"calibration","n":128,"egress_pps":6350,"valid":false,"violations":["rx lag"]}`)
		fmt.Println(`{"type":"receive_capacity","pps":3150,"lower_bound":true}`)
		return 0
	}
	return 2
}

func TestRunCalibration(t *testing.T) {
	t.Setenv("LEG_FAKE_LOADBOT", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cal, err := RunCalibration(ctx, CalConfig{
		LoadbotBin: exe, Seed: 7, Steps: "64,128", Hold: 3 * time.Second, Senders: 1, StderrDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Steps) != 2 || cal.ReceivePPS != 3150 || !cal.LowerBound {
		t.Fatalf("cal = %+v", cal)
	}
	// One sender per server CPU, never fewer than two.
	if cal.Senders != 2 {
		t.Errorf("Senders = %d, want 2", cal.Senders)
	}
	if cal.ReflectorSendErrors != 4 {
		t.Errorf("ReflectorSendErrors = %d, want 4 (read from the reflector's last line)", cal.ReflectorSendErrors)
	}
}

func TestRunCalibrationBadFlagsFail(t *testing.T) {
	t.Setenv("LEG_FAKE_LOADBOT", "1")
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A different seed makes the fake calibrate exit 2: the error must name it.
	_, err := RunCalibration(ctx, CalConfig{LoadbotBin: exe, Seed: 8, Steps: "64,128", Hold: 3 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "calibrate") {
		t.Fatalf("err = %v", err)
	}
}
