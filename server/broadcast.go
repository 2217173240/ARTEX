package server

import (
	"sync"

	"github.com/Autumn-27/artex/db"
)

// Broadcaster 按任务广播已保存的活动；数据库负责重连时的历史补偿。
type Broadcaster struct {
	mu   sync.Mutex
	subs map[string]map[chan db.Activity]struct{} // task id -> set of subscriber channels
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[string]map[chan db.Activity]struct{}{}}
}

// Subscribe returns a buffered channel of activities for a task plus an
// unsubscribe func the caller must invoke (defer) to release it.
func (b *Broadcaster) Subscribe(task string) (<-chan db.Activity, func()) {
	ch := make(chan db.Activity, 256)
	b.mu.Lock()
	if b.subs[task] == nil {
		b.subs[task] = map[chan db.Activity]struct{}{}
	}
	b.subs[task][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if m := b.subs[task]; m != nil {
				if _, exists := m[ch]; exists {
					delete(m, ch)
					close(ch)
				}
				if len(m) == 0 {
					delete(b.subs, task)
				}
			}
			b.mu.Unlock()
		})
	}
}

// Publish 非阻塞广播活动；缓冲满时先移除并关闭订阅，阻止后续帧越过缺口。
// handler 读完已排队的前缀后退出，客户端从最后收到的 ID 重连补偿。
// 此处保留发布顺序；数据库 ID 分配与并发发布的排序由调用方负责。
func (b *Broadcaster) Publish(task string, a db.Activity) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[task] {
		select {
		case ch <- a:
		default:
			delete(b.subs[task], ch)
			close(ch)
		}
	}
	if len(b.subs[task]) == 0 {
		delete(b.subs, task)
	}
}
