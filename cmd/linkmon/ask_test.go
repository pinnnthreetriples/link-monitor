package main

import (
	"strings"
	"testing"
)

func TestAskPromptReadsStdinOnlyForADash(t *testing.T) {
	t.Parallel()

	if got, _ := askPrompt("сделай X", strings.NewReader("ignored")); got != "сделай X" {
		t.Errorf("askPrompt(flag) = %q", got)
	}
	if got, _ := askPrompt("-", strings.NewReader("из stdin")); got != "из stdin" {
		t.Errorf("askPrompt(-) = %q", got)
	}
	if _, err := askPrompt("-", strings.NewReader("  \n")); err == nil {
		t.Error("askPrompt(-) with an empty stdin = nil error")
	}
}

// Values from outside reach the peer's PowerShell only as base64, so a
// directory with a quote in it cannot end the string and run something else.
func TestAskScriptCarriesValuesOnlyAsBase64(t *testing.T) {
	t.Parallel()

	script := askScript("AppData/Local/Temp/x.txt", `C:\it's; Remove-Item C:\`, "")
	if strings.Contains(script, "Remove-Item C:") || strings.Contains(script, "it's") {
		t.Errorf("a raw value reached the script:\n%s", script)
	}
	if strings.Contains(script, "@DIR@") || strings.Contains(script, "@REL@") {
		t.Errorf("a placeholder was left in the script:\n%s", script)
	}
}

func TestReadAskAnswerFindsClaudesJSONAmongNoise(t *testing.T) {
	t.Parallel()

	out := "#< CLIXML\r\n{\"result\":\"готово\",\"session_id\":\"d6a86863-4061-4a4d-85f7-454384d0f9fe\"," +
		"\"is_error\":false}\r\n"
	got, err := readAskAnswer(out)
	if err != nil || got.Result != "готово" || !sessionID.MatchString(got.SessionID) {
		t.Errorf("readAskAnswer() = %+v, %v", got, err)
	}
	if _, err := readAskAnswer("NOCLAUDE\r\n"); err == nil || !strings.Contains(err.Error(), "claude.exe") {
		t.Errorf("readAskAnswer(NOCLAUDE) = %v", err)
	}
	if _, err := readAskAnswer("что-то другое"); err == nil {
		t.Error("readAskAnswer(no JSON) = nil error")
	}
}
