package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/hostipc"
)

type blockingAnswerer struct{ seen chan context.Context }

func (a blockingAnswerer) Approve(ctx context.Context, _ string, _ ApprovalRequest) (string, error) {
	a.seen <- ctx
	<-ctx.Done()
	return AllowedOnce, nil
}

func (a blockingAnswerer) AnswerQuestions(ctx context.Context, _ string, _ QuestionRequest) ([]Answer, error) {
	return nil, nil
}

func newTestBridge(a Answerer, audit string) *Bridge {
	return &Bridge{Answerer: a, AuditLog: audit, cancels: map[string]pendingReq{}, early: map[string]time.Time{}}
}

// request.cancel that overtakes its request still closes the card.
func TestEarlyCancelIsKept(t *testing.T) {
	a := blockingAnswerer{seen: make(chan context.Context, 1)}
	b := newTestBridge(a, filepath.Join(t.TempDir(), "d.jsonl"))
	att := &hostipc.Attachment{Target: "rt"}
	b.Notify(att, "request.cancel", json.RawMessage(`{"requestId":"r1"}`))
	done := make(chan any, 1)
	go func() {
		res, _ := b.Request(context.Background(), att, "approval.ask", json.RawMessage(`{"requestId":"r1","toolName":"bash"}`))
		done <- res
	}()
	select {
	case res := <-done:
		if m, _ := res.(map[string]string); m["outcome"] != Cancelled {
			t.Fatalf("outcome = %v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("card stayed open after an early cancel")
	}
}

type allowAll struct{}

func (allowAll) Approve(context.Context, string, ApprovalRequest) (string, error) {
	return AllowedOnce, nil
}
func (allowAll) AnswerQuestions(context.Context, string, QuestionRequest) ([]Answer, error) {
	return nil, nil
}

// No record, no grant.
func TestApprovalFailsWhenAuditCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o644)
	b := newTestBridge(allowAll{}, filepath.Join(blocker, "logs", "d.jsonl")) // parent is a file
	res, err := b.Request(context.Background(), &hostipc.Attachment{Target: "rt"}, "approval.ask", json.RawMessage(`{"requestId":"r2","toolName":"bash"}`))
	if err == nil {
		t.Fatalf("granted without a record: %v", res)
	}
}
