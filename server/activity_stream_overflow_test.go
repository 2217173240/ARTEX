package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// 使用真实广播溢出前缀和 PostgreSQL 数据，经实际 HTTP 连接验证游标补偿。
// 此测试组合两个真实边界，不声称通过 TCP 背压确定性触发 handler 的溢出。
func TestActivityStreamReconnectAfterOverflowPrefix(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("SSE overflow declared fixture", "persisted replay fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := m.DeleteTask(task.ID, DeleteTaskOptions{}); err != nil {
			t.Error(err)
		}
	}()
	b := NewBroadcaster()
	ch, unsub := b.Subscribe(task.ID)
	defer unsub()
	ids := make([]int64, 800)
	for i := range ids {
		a := db.Activity{Kind: "text", Summary: fmt.Sprintf("declared fixture row %d", i)}
		id, err := task.Store.AppendActivity(a)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		a.ID = id
		b.Publish(task.ID, a)
	}
	var last int64
	count := 0
	for a := range ch {
		last = a.ID
		count++
	}
	if count != 256 || last != ids[255] {
		t.Fatalf("prefix count=%d last=%d", count, last)
	}
	s := &Server{m: m, engine: &Engine{bc: b}}
	httpServer := httptest.NewServer(http.HandlerFunc(s.streamActivity))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"?task="+task.ID+"&since="+strconv.FormatInt(ids[799], 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", strconv.FormatInt(last, 10))
	response, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
	scanner := bufio.NewScanner(response.Body)
	next := 256
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "id: ") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if id != ids[next] {
			t.Fatalf("replay index=%d ID=%d want=%d", next, id, ids[next])
		}
		next++
		if next == len(ids) {
			break
		}
	}
	if next != len(ids) {
		t.Fatalf("replayed to %d: %v", next, scanner.Err())
	}
	cancel()
	response.Body.Close()
	// Close 等待实际 handler 返回；随后检查取消释放了订阅。
	httpServer.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) != 0 {
		t.Fatal("HTTP cancellation retained subscriber")
	}
}
