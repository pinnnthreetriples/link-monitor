package tailscale

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// maxPort is the highest TCP port there is.
const maxPort = 65535

// urlPattern finds the tailnet URL in CLI output, used only as a fallback.
var urlPattern = regexp.MustCompile(`https://[^\s"'<>]+`)

// Serve publishes a local port to the tailnet over HTTPS and returns the URL
// peers can open. It runs in the background: the port stays published until
// [Client.ServeReset]. There is no LocalAPI shortcut worth taking here, so
// this one goes through the CLI.
func (c *Client) Serve(ctx context.Context, port int) (string, error) {
	if port < 1 || port > maxPort {
		return "", fmt.Errorf("port %d is outside 1-%d", port, maxPort)
	}

	res, err := c.runner.Run(ctx, "serve", "--bg", strconv.Itoa(port))
	if err != nil {
		return "", fmt.Errorf("tailscale serve %d: %w", port, err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("tailscale serve %d: exit %d: %s", port, res.Code, cliMessage(res))
	}

	// The daemon knows this node's name for certain; the printed URL is only a
	// fallback for when the daemon has gone quiet between the two calls.
	if url := c.selfURL(ctx); url != "" {
		return url, nil
	}
	if url := urlPattern.FindString(res.Stdout + "\n" + res.Stderr); url != "" {
		return url, nil
	}
	return "", errors.New("tailscale serve: the tailnet URL could not be determined")
}

// ServeReset withdraws everything this machine publishes to the tailnet.
func (c *Client) ServeReset(ctx context.Context) error {
	res, err := c.runner.Run(ctx, "serve", "reset")
	if err != nil {
		return fmt.Errorf("tailscale serve reset: %w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("tailscale serve reset: exit %d: %s", res.Code, cliMessage(res))
	}
	return nil
}

// selfURL builds this node's HTTPS address from the daemon's own view of it,
// and returns "" when the daemon cannot say.
func (c *Client) selfURL(ctx context.Context) string {
	st, err := c.status(ctx)
	if err != nil || st.Self == nil {
		return ""
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	if name == "" {
		return ""
	}
	return "https://" + name + "/"
}
