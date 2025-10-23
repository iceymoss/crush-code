package pubsub

import (
	"context"
	"sync"
)

// bufferSize 定义事件通道的大小
const bufferSize = 64

// Broker 代表发布和订阅事件的管理者
type Broker[T any] struct {
	subs      map[chan Event[T]]struct{} // 订阅者 subs本质上是一个订阅者通道的集合
	mu        sync.RWMutex               // 读写锁
	done      chan struct{}              // 停止信号
	subCount  int                        // 订阅者数量
	maxEvents int                        // 最大事件数量
}

// NewBroker 创建一个新的 Broker
func NewBroker[T any]() *Broker[T] {
	return NewBrokerWithOptions[T](bufferSize, 1000)
}

// NewBrokerWithOptions 创建一个新的 Broker，并指定缓冲大小和最大事件数量
func NewBrokerWithOptions[T any](channelBufferSize, maxEvents int) *Broker[T] {
	b := &Broker[T]{
		subs:      make(map[chan Event[T]]struct{}),
		done:      make(chan struct{}),
		subCount:  0,
		maxEvents: maxEvents,
	}
	return b
}

// Shutdown 关闭 Broker
func (b *Broker[T]) Shutdown() {
	select {
	case <-b.done: // 已经关闭, 不需要再次关闭
		return
	default: // 关闭，停止订阅
		close(b.done)
	}

	// 并发安全处理
	b.mu.Lock()
	defer b.mu.Unlock()

	// 关闭所有订阅者通道，关闭信息自动回被订阅者处理
	for ch := range b.subs {
		// 删除订阅者
		delete(b.subs, ch)

		// 关闭订阅者通道，并且自动通知订阅者，通道被关闭
		close(ch)
	}

	b.subCount = 0
}

// Subscribe 订阅事件
func (b *Broker[T]) Subscribe(ctx context.Context) <-chan Event[T] {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done: // 已经关闭, 返回一个已关闭的通道
		ch := make(chan Event[T])
		close(ch)
		return ch
	default:
	}

	// 创建一个订阅者通道
	sub := make(chan Event[T], bufferSize)
	b.subs[sub] = struct{}{}
	b.subCount++

	// 创建一个 goroutine，用于处理订阅者关闭事件
	go func() {
		// 上下文被cancel，关闭订阅者通道，并且自动通知订阅者，通道被关闭
		<-ctx.Done()

		b.mu.Lock()
		defer b.mu.Unlock()

		select {
		case <-b.done: // 已经关闭, 不需要处理
			return
		default:
		}

		// 删除订阅者
		delete(b.subs, sub)
		// 关闭订阅者通道，并且自动通知订阅者，通道被关闭
		close(sub)
		// 订阅者数量减一
		b.subCount--
	}()

	// 返回订阅者通道
	return sub
}

// GetSubscriberCount 获取订阅者数量
func (b *Broker[T]) GetSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.subCount
}

// Publish 发布事件
func (b *Broker[T]) Publish(t EventType, payload T) {
	b.mu.RLock()
	select {
	case <-b.done:
		b.mu.RUnlock()
		return
	default:
	}

	// 减少锁持有时间：如果直接在锁内遍历 b.subs并发送事件，锁会被长时间持有。chan本身是并发安全的，当订阅者很多或发送慢时，这会阻塞其他操作（如添加/删除订阅者）。
	// 避免死锁：如果订阅者处理事件时需要操作 Broker（如取消订阅），而发送事件时又持有锁，会导致死锁。
	// 线程安全：订阅者列表可能在发送过程中被修改（如删除订阅者）。复制后使用快照，保证当前发送的一致性。
	subscribers := make([]chan Event[T], 0, len(b.subs))
	for sub := range b.subs {
		subscribers = append(subscribers, sub)
	}
	b.mu.RUnlock()

	// 构造一个事件
	event := Event[T]{Type: t, Payload: payload}

	for _, sub := range subscribers {
		select {
		case sub <- event:
		default:
			// 频道已满，订阅者速度缓慢 - 跳过此活动
			// 这可以防止阻止发布者
		}
	}
}
