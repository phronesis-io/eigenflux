package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestSendPMRejectsInvalidUTF8Body(t *testing.T) {
	var requestContext app.RequestContext
	requestContext.Request.SetBody([]byte{'{', '"', 'c', 'o', 'n', 't', 'e', 'n', 't', '"', ':', '"', 0xff, '"', '}'})

	SendPM(context.Background(), &requestContext)

	if requestContext.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", requestContext.Response.StatusCode(), http.StatusBadRequest)
	}
	if body := string(requestContext.Response.Body()); !strings.Contains(body, "INVALID_TEXT_ENCODING") || !strings.Contains(body, "valid UTF-8") {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestSendPMRejectsReplacementCharacter(t *testing.T) {
	var requestContext app.RequestContext
	requestContext.Request.Header.SetContentTypeBytes([]byte("application/json"))
	requestContext.Request.SetBodyString(`{"content":"损坏�内容","item_id":"1"}`)

	SendPM(context.Background(), &requestContext)

	if requestContext.Response.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", requestContext.Response.StatusCode(), http.StatusBadRequest)
	}
	if body := string(requestContext.Response.Body()); !strings.Contains(body, "INVALID_TEXT_ENCODING") || !strings.Contains(body, "U+FFFD") {
		t.Fatalf("unexpected response body: %s", body)
	}
}
