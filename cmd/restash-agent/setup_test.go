package main

import "testing"

func TestCodeFromName(t *testing.T) {
	for name, want := range map[string]string{
		"restash-setup-K7Q2MX.exe":                             "K7Q2MX",
		"restash-setup-k7q2mx":                                 "K7Q2MX",
		"/home/anna/Downloads/restash-setup-K7Q2MX":            "K7Q2MX",
		`C:\Users\anna\Downloads\restash-setup-K7Q2MX (1).exe`: "K7Q2MX",
		"restash-setup-K7Q2MX(2).exe":                          "K7Q2MX",
		"restash-setup_K7Q2MX.exe":                             "K7Q2MX",
		"restash-setup.exe":                                    "",
		"restash-setup-K7Q2M.exe":                              "",
		"restash-setup-K7Q2MXY.exe":                            "",
		"restash-setup-K7Q2M0.exe":                             "", // 0 isn't in codes
		"restash-agent-linux-amd64":                            "",
	} {
		if got := codeFromName(name); got != want {
			t.Errorf("codeFromName(%q) = %q, want %q", name, got, want)
		}
	}
}
