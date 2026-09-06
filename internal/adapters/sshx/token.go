package sshx

import (
	"fmt"
	"strconv"
	"strings"
)

// Every script this package sends the peer answers with one line of the shape
//
//	<TOKEN> <int> [<int> ...]
//
// and is read with tokenLineFields. Two properties matter.
//
// The token and the values are ASCII chosen by us, so nothing the peer's
// display language does can change them. The peer runs a Russian Windows and
// every word Windows prints is translated; a number and a marker of our own are
// the only things that are not.
//
// And the line is *found*, not assumed to be the whole stream. Started from
// cmd.exe, PowerShell emits a CLIXML blob ("Preparing modules for first use")
// on a cold run, from the host, before the first line of the script executes -
// so no setting inside the script can prevent it. It was measured going to
// stderr, leaving stdout clean, but a parser that trusts a whole stream is one
// changed host away from reading that blob as a verdict. Finding the line costs
// nothing and removes the question.
//
// Run and RunPowerShell are deliberately not covered by this: they hand both
// streams back verbatim, because interpreting them is the caller's business.

// tokenLineFields finds the line of stdout beginning with token and returns its
// remaining want values as integers. Anything else on the stream is ignored.
func tokenLineFields(stdout, token string, want int) ([]int, error) {
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != token {
			continue
		}
		if len(fields) != want+1 {
			return nil, fmt.Errorf("the %s line carries %d values, expected %d",
				token, len(fields)-1, want)
		}

		values := make([]int, want)
		for i := range values {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil {
				return nil, fmt.Errorf("value %d of the %s line is not a number: %w",
					i+1, token, err)
			}
			values[i] = n
		}
		return values, nil
	}
	return nil, fmt.Errorf("the peer printed no %s line", token)
}
