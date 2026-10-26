package agent

import (
	"strings"
	"testing"
)

func TestCodexReadLoopAcceptsResponseLargerThanTwentyMiB(t *testing.T) {
	client := NewCodexAppServerClient("", nil)
	responseCh := make(chan codexResponse, 1)
	client.pending["1"] = responseCh

	largeText := strings.Repeat("x", 21*1024*1024)
	wire := `{"id":1,"result":{"thread":{"id":"thread-large"},"payload":"` + largeText + `"}}` + "\n"
	client.readLoop(strings.NewReader(wire))

	select {
	case response := <-responseCh:
		if response.IDKey != "1" {
			t.Fatalf("response ID = %q, want 1", response.IDKey)
		}
		if !strings.Contains(string(response.Result), `"id":"thread-large"`) {
			t.Fatal("large response was not delivered to the pending request")
		}
	default:
		t.Fatal("large response was not delivered to the pending request")
	}
	if client.readErr == nil || client.readErr.Error() != "EOF" {
		t.Fatalf("readErr = %v, want EOF after complete response", client.readErr)
	}
}
