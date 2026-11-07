package service

import (
	"strings"
	"testing"
)

func TestUnit(t *testing.T) {
	u := Unit("/home/anna/.local/share/restash/restash-agent")
	if !strings.Contains(u, "ExecStart=/home/anna/.local/share/restash/restash-agent run -service\n") {
		t.Fatal(u)
	}
	if u := Unit("/home/an na/x"); !strings.Contains(u, `ExecStart="/home/an na/x" run`) {
		t.Fatal(u)
	}
}
