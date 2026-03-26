package pubsub

import (
	"context"
	"sync"
)

// bufferSize 定义了缓冲区大小
const bufferSize = 64

// Broker 定义了发布者/订阅者模式中的发布者，他实现了Subscriber[T any]和Publisher[T any]接口接口
// T 是泛型，表示事件的类型
// subs 是订阅者列表，用于存储订阅者
// mu 是互斥锁，用于保护subs
// done 是关闭通道，用于关闭发布者
// subCount 是订阅者数量
// maxEvents 是最大事件数量
type Broker[T any] struct {
	subs      map[chan Event[T]]struct{} // 订阅者列表，用于存储订阅者, 可能是会话、消息、权限等类型的订阅者
	mu        sync.RWMutex               // 互斥锁，用于保护subs
	done      chan struct{}              // 关闭通道信号同步，用于关闭发布者
	subCount  int                        // 订阅者数量
	maxEvents int                        // 最大事件数量
}

// NewBroker 创建一个发布者/订阅者模式中的发布者
func NewBroker[T any]() *Broker[T] {
	return NewBrokerWithOptions[T](bufferSize, 1000)
}

// NewBrokerWithOptions 创建一个发布者/订阅者模式中的发布者，并设置缓冲区大小和最大事件数量
// channelBufferSize 是缓冲区大小
// maxEvents 是最大事件数量
func NewBrokerWithOptions[T any](channelBufferSize, maxEvents int) *Broker[T] {
	return &Broker[T]{
		subs:      make(map[chan Event[T]]struct{}),
		done:      make(chan struct{}),
		maxEvents: maxEvents,
	}
}

func (b *Broker[T]) Shutdown() {
	select {
	case <-b.done: // 如果已经关闭，则返回，防止重复关闭
		return
	default: // 如果未关闭，则关闭done通道
		close(b.done)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// 遍历订阅者列表，关闭每个订阅者通道
	for ch := range b.subs {
		delete(b.subs, ch) // 将订阅通道从订阅者列表中删除
		close(ch)          // 关闭订阅通道
	}

	b.subCount = 0 // 重置订阅者数量
}

// Subscribe 订阅一个事件, 传入一个上下文，然后返回一个事件通道，用于接收事件
// ctx 是上下文，用于取消订阅
// <-chan Event[T] 是事件通道，用于接收事件
func (b *Broker[T]) Subscribe(ctx context.Context) <-chan Event[T] {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done: // 如果已经关闭，则返回一个空的事件通道
		ch := make(chan Event[T]) // 创建一个空的事件通道
		close(ch)                 // 关闭事件通道
		return ch                 // 返回一个已经关闭的事件通道
	default:
	}

	// 创建一个事件有缓冲的通道，用于接收事件
	sub := make(chan Event[T], bufferSize)
	b.subs[sub] = struct{}{} // 将订阅通道添加到订阅者列表中
	b.subCount++             // 增加订阅者数量

	// 开启一个goroutine，监听上下文取消信号，如果取消，则删除当前主函数的创建的订阅通道，并关闭通道
	// 目的：保证没发生一次订阅，就有对应子协程对上下文进行监控并且做清理工作
	go func() {
		<-ctx.Done() // 监听上下文取消信号

		b.mu.Lock()
		defer b.mu.Unlock()

		select {
		case <-b.done: // 如果已经关闭，则返回
			return
		default:
		}

		delete(b.subs, sub) // 当前主函数的创建的订阅通道，从订阅者列表中删除
		close(sub)          // 关闭订阅通道
		b.subCount--        // 减少订阅者数量
	}()

	// 返回订阅通道，用于接收事件
	return sub
}

// GetSubscriberCount 获取订阅者数量
func (b *Broker[T]) GetSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.subCount
}

// Publish 发布一个事件, 传入一个事件类型和事件的负载，然后发布事件
func (b *Broker[T]) Publish(t EventType, payload T) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	select {
	case <-b.done: // 如果已经关闭，则返回
		return
	default:
	}

	// 构造一个对应事件的数据结构
	event := Event[T]{Type: t, Payload: payload}

	// 遍历订阅者列表，发布事件到每个订阅者通道
	for sub := range b.subs {
		select {
		case sub <- event:
		default:
			// Channel is full, subscriber is slow - skip this event
			// This prevents blocking the publisher
		}
	}
}
