package db

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// InsertSettingIfAbsent 是 auth.password_hash 的兜底：调用方的 GetSetting 检查可能
// 因为数据库报错而失效，也可能被并发请求插队（bcrypt 要跑几十毫秒），所以"仅首次
// 可设"的保证必须落在主键约束上，而不是上层的 if 判断。
func TestInsertSettingIfAbsentDoesNotOverwrite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	// 用测试专属 key，绝不碰开发库里真实的 auth.password_hash。
	key := fmt.Sprintf("test.insert_if_absent.%d", time.Now().UnixNano())
	defer func() { _, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key) }()

	inserted, err := d.InsertSettingIfAbsent(key, "first")
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("首次写入应返回 inserted=true")
	}

	inserted, err = d.InsertSettingIfAbsent(key, "second")
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("键已存在时应返回 inserted=false")
	}

	got, ok, err := d.GetSetting(key)
	if err != nil || !ok {
		t.Fatalf("GetSetting: ok=%v err=%v", ok, err)
	}
	if got != "first" {
		t.Fatalf("值被覆盖成 %q，应保持 %q", got, "first")
	}
}

func TestCompareAndSetSettingConcurrentChange(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	key := fmt.Sprintf("test.compare_and_set.%d", time.Now().UnixNano())
	defer func() { _, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key) }()
	if err := d.SetSetting(key, "authenticated-old-value"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan bool, 16)
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			changed, err := d.CompareAndSetSetting(key, "authenticated-old-value", fmt.Sprintf("replacement-%d", i))
			results <- changed
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	changes := 0
	for changed := range results {
		if changed {
			changes++
		}
	}
	if changes != 1 {
		t.Fatalf("concurrent replacements=%d want 1", changes)
	}
	value, ok, err := d.GetSetting(key)
	if err != nil || !ok || value == "authenticated-old-value" {
		t.Fatalf("winning replacement missing: %q %v", value, err)
	}
	changed, err := d.CompareAndSetSetting(key, "authenticated-old-value", "stale-overwrite")
	if err != nil || changed {
		t.Fatalf("stale update accepted=%v err=%v", changed, err)
	}
	unchanged, _, err := d.GetSetting(key)
	if err != nil || unchanged != value {
		t.Fatal("stale update changed winning value")
	}
}
